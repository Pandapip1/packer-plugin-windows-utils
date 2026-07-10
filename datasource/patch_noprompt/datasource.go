package patch_noprompt

import (
	"fmt"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/zclconf/go-cty/cty"
)

type Datasource struct {
	isoPath string
}

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"iso_path": &hcldec.AttrSpec{Name: "iso_path", Type: cty.String, Required: true},
	}
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"patched_iso_path": &hcldec.AttrSpec{Name: "patched_iso_path", Type: cty.String},
	}
}

func (d *Datasource) Configure(configs ...interface{}) error {
	for _, raw := range configs {
		cval, ok := raw.(cty.Value)
		if !ok || cval.IsNull() || !cval.IsKnown() {
			continue
		}
		if v := cval.GetAttr("iso_path"); v.IsKnown() && !v.IsNull() {
			d.isoPath = v.AsString()
		}
	}
	if d.isoPath == "" {
		return fmt.Errorf("iso_path is required")
	}
	return nil
}

func (d *Datasource) Execute() (cty.Value, error) {
	path, err := patchISO(d.isoPath)
	if err != nil {
		return cty.NilVal, err
	}
	return cty.ObjectVal(map[string]cty.Value{
		"patched_iso_path": cty.StringVal(path),
	}), nil
}
