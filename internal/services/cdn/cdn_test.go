package cdn

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/hashicorp/terraform-plugin-framework/attr"
	"github.com/hashicorp/terraform-plugin-framework/diag"
	"github.com/hashicorp/terraform-plugin-framework/resource"
	"github.com/hashicorp/terraform-plugin-framework/types"
)

func model() distributionModel {
	return distributionModel{Name: types.StringValue("web"), Description: types.StringNull(), Enabled: types.BoolValue(true),
		Tags:   types.MapValueMust(types.StringType, map[string]attr.Value{"team": types.StringValue("web")}),
		Origin: &origin{Host: types.StringValue("origin.example.com"), Protocol: types.StringValue("https"), Port: types.Int64Value(443), HostHeader: types.StringValue("viewer")}}
}

func errorsOf(d diag.Diagnostics) string {
	var out []string
	for _, e := range d.Errors() {
		out = append(out, e.Summary()+": "+e.Detail())
	}
	return strings.Join(out, "\n")
}

func TestValidateAtPlan(t *testing.T) {
	if d := validate(model()); d.HasError() {
		t.Fatal(errorsOf(d))
	}
	cases := map[string]func(*distributionModel){
		"protocol":    func(m *distributionModel) { m.Origin.Protocol = types.StringValue("ftp") },
		"host_header": func(m *distributionModel) { m.Origin.HostHeader = types.StringValue("client") },
		"port":        func(m *distributionModel) { m.Origin.Port = types.Int64Value(70000) },
		"11 domains": func(m *distributionModel) {
			for i := range 11 {
				m.Domains = append(m.Domains, domain{Hostname: types.StringValue(string(rune('a'+i)) + ".example.com"), CertificateID: types.StringValue("crt_2m9x0a7qpe4k")})
			}
		},
		"uppercase hostname": func(m *distributionModel) {
			m.Domains = []domain{{Hostname: types.StringValue("WWW.example.com"), CertificateID: types.StringValue("crt_2m9x0a7qpe4k")}}
		},
		"duplicate hostname": func(m *distributionModel) {
			d := domain{Hostname: types.StringValue("www.example.com"), CertificateID: types.StringValue("crt_2m9x0a7qpe4k")}
			m.Domains = []domain{d, d}
		},
		"name with spaces": func(m *distributionModel) { m.Name = types.StringValue("web ") },
		"certificate": func(m *distributionModel) {
			m.Domains = []domain{{Hostname: types.StringValue("www.example.com"), CertificateID: types.StringValue("lbr_2m9x0a7qpe4k")}}
		},
	}
	for name, change := range cases {
		m := model()
		change(&m)
		if !validate(m).HasError() {
			t.Errorf("%s: no error at plan", name)
		}
	}
	// Unknown values (from other resources) wait for apply.
	m := model()
	m.Origin.Protocol = types.StringUnknown()
	m.Domains = []domain{{Hostname: types.StringUnknown(), CertificateID: types.StringUnknown()}}
	if d := validate(m); d.HasError() {
		t.Error(errorsOf(d))
	}
	// Ten is the limit, not over it.
	m = model()
	for i := range 10 {
		m.Domains = append(m.Domains, domain{Hostname: types.StringValue(string(rune('a'+i)) + ".example.com"), CertificateID: types.StringValue("crt_2m9x0a7qpe4k")})
	}
	if d := validate(m); d.HasError() {
		t.Error(errorsOf(d))
	}
}

// The body is the whole spec, every field present, so a replace resets nothing by leaving it out.
func TestBodyIsTheWholeSpec(t *testing.T) {
	m := model()
	var d diag.Diagnostics
	got, _ := json.Marshal(m.body(context.Background(), &d))
	want := `{"description":null,"domains":[],"enabled":true,"name":"web","origin":{"host":"origin.example.com","hostHeader":"viewer","port":443,"protocol":"https"},"tags":{"team":"web"}}`
	if d.HasError() || string(got) != want {
		t.Errorf("%s\nwant %s", got, want)
	}
}

func TestSchema(t *testing.T) {
	var sr resource.SchemaResponse
	(&distributionResource{}).Schema(context.Background(), resource.SchemaRequest{}, &sr)
	for _, name := range []string{"id", "default_hostname", "status", "resource_name"} {
		if a := sr.Schema.Attributes[name]; !a.IsComputed() || a.IsOptional() || a.IsRequired() {
			t.Errorf("%s isn't computed only", name)
		}
	}
	if _, ok := sr.Schema.Blocks["domain"]; !ok {
		t.Error("no domain block")
	}
}
