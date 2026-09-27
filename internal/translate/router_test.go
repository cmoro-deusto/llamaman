package translate

import (
	"reflect"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

func TestRouterBuildArgv(t *testing.T) {
	globals := config.Globals{Binaries: []config.Binary{{Name: "default", Path: "/usr/bin/llama-server"}}, DefaultBinary: "default", Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080}
	res := RouterBuild(globals, "/home/me/my-models.ini", nil, "/usr/bin/llama-server")
	want := []string{
		"/usr/bin/llama-server",
		"--models-preset", "/home/me/my-models.ini",
		"--host", "127.0.0.1",
		"--port", "9080",
		"--metrics",
	}
	if !reflect.DeepEqual(res.Argv, want) {
		t.Errorf("RouterBuild argv = %v, want %v", res.Argv, want)
	}
}
