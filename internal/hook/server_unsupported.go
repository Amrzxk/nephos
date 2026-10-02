//go:build !linux

package hook

import (
	"context"
	"fmt"
	"net"
	"net/http"

	"github.com/Amrzxk/nephos/internal/store"
)

type ENIPlumber interface{}

func NewHandler(*store.Store, ENIPlumber) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Error(w, "hook requires Linux", 503) })
}
func Listen(context.Context, string) (net.Listener, error) {
	return nil, fmt.Errorf("private hook server requires Linux peer credentials")
}
