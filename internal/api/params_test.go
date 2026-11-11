package api

import "testing"

// Names in a path are decoded once. A ".." segment never reaches the handler, because the path is cleaned before
// routing since Fiber 3.6 / fasthttp 1.75 (404 from the router); other odd names reach it and are refused there.
func TestPathParameters(t *testing.T) {
	f := newHarness(t)
	for _, name := range []string{"..", "%2e%2e", "%2E%2E", ".%2e", "%2e."} {
		f.send("DELETE", "/users/"+name, nil, admin).wantError(t, 404, "not_found")
		f.send("POST", "/users/"+name+"/password", map[string]string{"password": "x"}, admin).wantError(t, 404, "not_found")
		f.send("GET", "/jobs/"+name, nil, admin).wantError(t, 404, "not_found")
		f.send("POST", "/jobs/"+name+"/cancel", nil, admin).wantError(t, 404, "not_found")
	}
	for _, name := range []string{"a%20b", "a+b", "a%2Fb", "a%252Fb"} {
		f.send("DELETE", "/users/"+name, nil, admin).wantError(t, 404, "not_found")
		f.send("POST", "/users/"+name+"/password", map[string]string{"password": "x"}, admin).wantError(t, 400, "invalid_input")
		f.send("GET", "/jobs/"+name, nil, admin).wantError(t, 404, "not_found")
	}
}
