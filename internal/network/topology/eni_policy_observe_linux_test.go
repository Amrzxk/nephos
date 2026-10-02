package topology

import (
	"encoding/json"
	"net/netip"
	"strings"
	"testing"
)

// Recorded from nftables 1.1.3 inside the isolated appliance. Keep independent
// of eniPolicyShape so the renderer/observer contract cannot pass by tautology.
const observedENIPolicy = `{"nftables":[
 {"metainfo":{"version":"1.1.3","json_schema_version":1}},
 {"table":{"family":"inet","name":"nephos","handle":91,"comment":"nephos:m1"}},
 {"chain":{"family":"inet","table":"nephos","name":"source_check","handle":1,"type":"filter","hook":"prerouting","prio":-300,"policy":"accept"}},
 {"set":{"family":"inet","name":"eni_sources","table":"nephos","type":["ifname","ipv4_addr"],"handle":2,"elem":[{"concat":["ve2","10.0.2.4"]},{"concat":["ve1","10.0.1.4"]}]}},
 {"rule":{"family":"inet","table":"nephos","chain":"source_check","handle":3,"expr":[{"match":{"op":"==","left":{"concat":[{"meta":{"key":"iifname"}},{"payload":{"protocol":"ip","field":"saddr"}}]},"right":"@eni_sources"}},{"return":null}]}},
 {"rule":{"family":"inet","table":"nephos","chain":"source_check","handle":4,"comment":"nephos:anti-spoof","expr":[{"match":{"op":"==","left":{"meta":{"key":"iifname"}},"right":"ve*"}},{"counter":{"packets":72,"bytes":9000}},{"drop":null}]}}
]}`

func TestENIPolicyObservation(t *testing.T) {
	records := map[string]eniIdentity{
		"ve1": {IP: netip.MustParseAddr("10.0.1.4")},
		"ve2": {IP: netip.MustParseAddr("10.0.2.4")},
	}
	for _, tc := range []struct {
		name, input string
		want        bool
	}{
		{"healthy handles/counters and unordered set", observedENIPolicy, true},
		{"priority drift", strings.Replace(observedENIPolicy, `"prio":-300`, `"prio":0`, 1), false},
		{"wrong source", strings.Replace(observedENIPolicy, "10.0.1.4", "10.0.1.5", 1), false},
		{"allow instead of drop", strings.Replace(observedENIPolicy, `"drop":null`, `"accept":null`, 1), false},
		{"wrong ownership", strings.Replace(observedENIPolicy, "nephos:m1", "foreign", 1), false},
		{"unexpected counter property", strings.Replace(observedENIPolicy, `"packets":72`, `"packets":72,"name":"foreign"`, 1), false},
		{"incomplete", `{"nftables":[]}`, false},
		{"invalid JSON", `{`, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := eniPolicyMatches(records, []byte(tc.input)); got != tc.want {
				t.Fatalf("matches=%v want=%v", got, tc.want)
			}
		})
	}
	var observed struct{ Nftables []eniPolicyEntry }
	if err := json.Unmarshal([]byte(observedENIPolicy), &observed); err != nil {
		t.Fatal(err)
	}
	observed.Nftables[4], observed.Nftables[5] = observed.Nftables[5], observed.Nftables[4]
	data, err := json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	if eniPolicyMatches(records, data) {
		t.Fatal("wrong rule order accepted")
	}
	observed.Nftables[4], observed.Nftables[5] = observed.Nftables[5], observed.Nftables[4]
	observed.Nftables = append(observed.Nftables, observed.Nftables[5])
	data, err = json.Marshal(observed)
	if err != nil {
		t.Fatal(err)
	}
	if eniPolicyMatches(records, data) {
		t.Fatal("extra owned rule accepted")
	}
}
