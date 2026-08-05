package translate

import (
	"reflect"
	"testing"

	"github.com/cmoro-deusto/llamaman/internal/config"
)

func TestRouterBuildArgv(t *testing.T) {
	globals := config.Globals{Bin: "/usr/bin/llama-server", Host: "127.0.0.1", Port: 9080}
	res := RouterBuild(globals, "/home/me/my-models.ini", nil)
	want := []string{
		"/usr/bin/llama-server",
		"--models-preset", "/home/me/my-models.ini",
		"--host", "127.0.0.1",
		"--port", "9080",
	}
	if !reflect.DeepEqual(res.Argv, want) {
		t.Errorf("RouterBuild argv = %v, want %v", res.Argv, want)
	}
}
