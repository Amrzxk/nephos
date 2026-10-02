package topology

import (
	"encoding/json"
	"reflect"
	"sort"
)

// This is the structural nft JSON equivalent of renderENISourceChecks.
// Handles and accumulated counter values are observations, not desired policy.
const eniPolicyShape = `[
 {"table":{"family":"inet","name":"nephos","comment":"nephos:m1"}},
 {"chain":{"family":"inet","table":"nephos","name":"source_check","type":"filter","hook":"prerouting","prio":-300,"policy":"accept"}},
 {"set":{"family":"inet","name":"eni_sources","table":"nephos","type":["ifname","ipv4_addr"]}},
 {"rule":{"family":"inet","table":"nephos","chain":"source_check","expr":[
  {"match":{"op":"==","left":{"concat":[{"meta":{"key":"iifname"}},{"payload":{"protocol":"ip","field":"saddr"}}]},"right":"@eni_sources"}},{"return":null}]}},
 {"rule":{"family":"inet","table":"nephos","chain":"source_check","comment":"nephos:anti-spoof","expr":[
  {"match":{"op":"==","left":{"meta":{"key":"iifname"}},"right":"ve*"}},{"counter":{}},{"drop":null}]}}
]`

type eniPolicyEntry map[string]map[string]any

// eniPolicyMatches is pure and rejects unknown/extra owned policy objects.
// Other tables and their rules are outside this engine's ownership.
func eniPolicyMatches(records map[string]eniIdentity, data []byte) bool {
	var observed struct{ Nftables []eniPolicyEntry }
	if err := json.Unmarshal(data, &observed); err != nil {
		return false
	}
	var expected []eniPolicyEntry
	if err := json.Unmarshal([]byte(eniPolicyShape), &expected); err != nil {
		return false
	}
	names := make([]string, 0, len(records))
	for name := range records {
		names = append(names, name)
	}
	sort.Strings(names)
	elements := make([]any, 0, len(names))
	for _, name := range names {
		elements = append(elements, map[string]any{"concat": []any{name, records[name].IP.String()}})
	}
	if len(elements) > 0 {
		expected[2]["set"]["elem"] = elements
	}
	var table, chain, set eniPolicyEntry
	var rules []eniPolicyEntry
	for _, entry := range observed.Nftables {
		if len(entry) != 1 {
			return false
		}
		for kind, object := range entry {
			owned := object["family"] == "inet" && object["table"] == "nephos"
			if kind == "table" {
				owned = object["family"] == "inet" && object["name"] == "nephos"
			}
			if !owned {
				continue
			}
			delete(object, "handle")
			switch kind {
			case "table":
				if table != nil {
					return false
				}
				table = entry
			case "chain":
				if chain != nil {
					return false
				}
				chain = entry
			case "set":
				if set != nil || !sortENIElements(object) {
					return false
				}
				set = entry
			case "rule":
				if !normalizeENICounters(object) {
					return false
				}
				rules = append(rules, entry)
			default:
				return false
			}
		}
	}
	actual := append([]eniPolicyEntry{table, chain, set}, rules...)
	return reflect.DeepEqual(actual, expected)
}

func sortENIElements(object map[string]any) bool {
	elements, exists := object["elem"]
	if !exists {
		return true
	}
	list, ok := elements.([]any)
	if !ok {
		return false
	}
	keys := make(map[string]any, len(list))
	var names []string
	for _, element := range list {
		item, ok := element.(map[string]any)
		if !ok || len(item) != 1 {
			return false
		}
		concat, ok := item["concat"].([]any)
		if !ok || len(concat) != 2 {
			return false
		}
		name, ok := concat[0].(string)
		if !ok || keys[name] != nil {
			return false
		}
		keys[name] = element
		names = append(names, name)
	}
	sort.Strings(names)
	for i, name := range names {
		list[i] = keys[name]
	}
	return true
}

func normalizeENICounters(object map[string]any) bool {
	exprs, ok := object["expr"].([]any)
	if !ok {
		return false
	}
	for _, raw := range exprs {
		expr, ok := raw.(map[string]any)
		if !ok {
			return false
		}
		if counter, exists := expr["counter"]; exists {
			values, ok := counter.(map[string]any)
			if !ok {
				return false
			}
			delete(values, "packets")
			delete(values, "bytes")
		}
	}
	return true
}
