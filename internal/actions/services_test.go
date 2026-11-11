package actions_test

import (
	"os"
	"reflect"
	"testing"

	"github.com/Pukka922/edelweiss-ext-samba/internal/accounts"
	"github.com/Pukka922/edelweiss-ext-samba/internal/actions"
)

// The API makes the service account of a folder whose owner has none (synchronously); the job then renders the config
// again, which now has an owner for the share.
func TestServiceAccount(t *testing.T) {
	f := newFixture(t)
	f.with(f.fotos())
	spec := f.plan(actions.Request{Kind: "service-account", Path: "/mnt/disk/data"})
	same(t, "service-account", describe(spec.Steps), []string{writeStaged, testparm, shareACL("Fotos", annaWrite+","+benRead), writeConfig, writeConf, reload})
	if got := written(t, spec); !reflect.DeepEqual(got, f.env.Config) {
		t.Errorf("config %+v", got)
	}
	if spec.Target != "/mnt/disk/data" || !reflect.DeepEqual(spec.Uses, []string{"share:Fotos"}) {
		t.Errorf("target %q, uses %v", spec.Target, spec.Uses)
	}
	code(t, f.refused(actions.Request{Kind: "service-account", Path: "/mnt/disk/more"}), actions.CodeNotFound, map[string]any{"path": "/mnt/disk/more"})
	f.env.DB = &accounts.DB{}
	code(t, f.refused(actions.Request{Kind: "service-account", Path: "/mnt/disk/data"}), actions.CodeNoOwner, map[string]any{"uid": os.Getuid(), "gid": os.Getgid()})
}
