package jobs_test

import (
	"encoding/json"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/Pukka922/edelweiss-ext-samba/internal/jobs"
	"github.com/Pukka922/edelweiss-ext-samba/internal/run"
)

// control is the command of start, stop and restart: rc-service <service> <command>, with the time they get (a slow
// stop of docker).
func control(service, command string) *run.Cmd {
	return &run.Cmd{Path: "/sbin/rc-service", Args: []string{service, command}, Timeout: 5 * time.Minute}
}

// rcUpdate is rc-update <command> <service> <runlevel>.
func rcUpdate(command, service, runlevel string) *run.Cmd {
	return &run.Cmd{Path: "/sbin/rc-update", Args: []string{command, service, runlevel}, Timeout: 30 * time.Second}
}

// lbuCommit is the command that saves the changes of /etc in diskless and data mode.
func lbuCommit() *run.Cmd {
	return &run.Cmd{Path: "/usr/sbin/lbu", Args: []string{"commit"}, Timeout: 30 * time.Second}
}

// startStep, stopStep and restartStep are exec steps of rc-service.
func startStep(service string) jobs.Step {
	return jobs.Step{Op: "exec", Label: "start", Cmd: control(service, "start")}
}

func stopStep(service string) jobs.Step {
	return jobs.Step{Op: "exec", Label: "stop", Cmd: control(service, "stop")}
}

func restartStep(service string) jobs.Step {
	return jobs.Step{Op: "exec", Label: "restart", Cmd: control(service, "restart")}
}

func TestDescribe(t *testing.T) {
	for _, c := range []struct {
		step jobs.Step
		want string
	}{
		{jobs.Step{Op: "exec", Label: "start", Cmd: control("sshd", "start")}, "exec /sbin/rc-service sshd start"},
		{jobs.Step{Op: "exec", Label: "enable", Cmd: rcUpdate("add", "sshd", "default")}, "exec /sbin/rc-update add sshd default"},
		{jobs.Step{Op: "exec", Label: "lbu", Cmd: lbuCommit()}, "exec /usr/sbin/lbu commit"},
		{jobs.Step{Op: "exec"}, "exec"},
		{jobs.Step{Op: "reboot"}, "reboot"},
		{startStep("sshd"), "exec /sbin/rc-service sshd start"},
		{stopStep("net.eth0"), "exec /sbin/rc-service net.eth0 stop"},
		{restartStep("docker"), "exec /sbin/rc-service docker restart"},
		{jobs.Step{Op: "exec", Label: "restart", Cmd: control("docker", "restart"), Ignore: true}, "exec /sbin/rc-service docker restart"},
		// a write step is its path: the content, the mode and the backup do not show
		{jobs.Step{Op: "write", Label: "write", Path: "/etc/network/interfaces", Data: []byte("secret"), Mode: 0o600, Backup: "/etc/network/interfaces.bak"}, "write /etc/network/interfaces"},
		{jobs.Step{Op: "write"}, "write"},
		{jobs.Step{Op: "await", Label: "confirm", Wait: 120 * time.Second}, "await 2m0s"},
		{jobs.Step{Op: "await"}, "await"},
	} {
		if got := jobs.Describe(c.step); got != c.want {
			t.Errorf("Describe(%+v)\n got %q\nwant %q", c.step, got, c.want)
		}
	}
}

// A step is part of spec.json.
func TestStepJSON(t *testing.T) {
	data, err := json.Marshal(startStep("sshd"))
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"op":"exec","label":"start","cmd":{"path":"/sbin/rc-service","args":["sshd","start"],"timeout":300000000000}}`
	if string(data) != want {
		t.Errorf("JSON\n got %s\nwant %s", data, want)
	}
	var got jobs.Step
	if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, startStep("sshd")) {
		t.Errorf("round trip: %+v, %v", got, err)
	}
}

// The content of a write step is base64 in spec.json, its mode a number, the wait of an await step nanoseconds; what a
// step does not use is not there.
func TestStepJSONWriteAndAwait(t *testing.T) {
	for _, c := range []struct {
		name string
		step jobs.Step
		want string
	}{
		{"write", jobs.Step{Op: "write", Label: "write", Path: "/etc/x", Data: []byte("hi"), Mode: 0o600, Backup: "/etc/x.bak"},
			`{"op":"write","label":"write","path":"/etc/x","data":"aGk=","mode":384,"backup":"/etc/x.bak"}`},
		{"write without mode and backup", jobs.Step{Op: "write", Label: "write", Path: "/etc/x", Data: []byte("hi")},
			`{"op":"write","label":"write","path":"/etc/x","data":"aGk="}`},
		{"await", jobs.Step{Op: "await", Label: "confirm", Wait: 120 * time.Second},
			`{"op":"await","label":"confirm","wait":120000000000}`},
	} {
		t.Run(c.name, func(t *testing.T) {
			data, err := json.Marshal(c.step)
			if err != nil || string(data) != c.want {
				t.Errorf("JSON\n got %s, %v\nwant %s", data, err, c.want)
			}
			var got jobs.Step
			if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, c.step) {
				t.Errorf("round trip: %+v, %v", got, err)
			}
		})
	}
	// binary content survives
	bin := jobs.Step{Op: "write", Label: "write", Path: "/x", Data: []byte{0, 1, 0xff, '\n', '"'}}
	data, _ := json.Marshal(bin)
	var got jobs.Step
	if err := json.Unmarshal(data, &got); err != nil || !reflect.DeepEqual(got, bin) {
		t.Errorf("binary content: %+v, %v", got, err)
	}
}

// The steps to undo a failed job are part of spec.json as "onFail", and only when there are some.
func TestSpecJSONOnFail(t *testing.T) {
	data, err := json.Marshal(jobs.Spec{ID: "i", Kind: "k", Target: "t", Steps: []jobs.Step{{Op: "await", Label: "confirm", Wait: time.Second}},
		OnFail: []jobs.Step{{Op: "write", Label: "write", Path: "/x"}}})
	if err != nil {
		t.Fatal(err)
	}
	const want = `{"id":"i","kind":"k","target":"t","steps":[{"op":"await","label":"confirm","wait":1000000000}],` +
		`"onFail":[{"op":"write","label":"write","path":"/x"}]}`
	if string(data) != want {
		t.Errorf("JSON\n got %s\nwant %s", data, want)
	}
	plain, err := json.Marshal(jobs.Spec{ID: "i", Kind: "k", Target: "t", Steps: []jobs.Step{}})
	if err != nil || string(plain) != `{"id":"i","kind":"k","target":"t","steps":[]}` {
		t.Errorf("JSON without onFail = %s, %v", plain, err)
	}
}

// The nonce and the deadline of a state are there only while the job waits.
func TestStateJSONAwaiting(t *testing.T) {
	deadline := time.Date(2026, 10, 22, 12, 2, 0, 0, time.UTC)
	st := jobs.State{ID: "i", Status: jobs.Running, Steps: []string{"confirm"}, Started: deadline.Add(-2 * time.Minute), Nonce: "0123456789abcdef0123456789abcdef", Deadline: &deadline}
	data, err := json.Marshal(st)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), `"nonce":"0123456789abcdef0123456789abcdef","deadline":"2026-10-22T12:02:00Z"`) {
		t.Errorf("JSON of a waiting state: %s", data)
	}
	st.Nonce, st.Deadline = "", nil
	if data, err = json.Marshal(st); err != nil || strings.Contains(string(data), "nonce") || strings.Contains(string(data), "deadline") {
		t.Errorf("JSON of a state that does not wait: %s, %v", data, err)
	}
}
