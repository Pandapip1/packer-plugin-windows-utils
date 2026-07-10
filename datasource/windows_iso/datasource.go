package windows_iso

import (
	"fmt"
	"time"

	"github.com/hashicorp/hcl/v2/hcldec"
	"github.com/zclconf/go-cty/cty"
)

// defaultCacheTTL is used when cache_ttl isn't set. It's short because the
// download URLs Microsoft hands out are themselves short-lived signed links;
// the cache exists to avoid repeat handshakes against Microsoft's servers
// (which risk a 715-123130 IP ban) within a single burst of builds/retries,
// not to stand in for pinning the release/edition/language/arch fields.
const defaultCacheTTL = "1h"

type Datasource struct {
	version  string
	release  string
	edition  string
	language string
	arch     string
	locale   string
	cacheTTL time.Duration
}

func (d *Datasource) ConfigSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"version":   &hcldec.AttrSpec{Name: "version", Type: cty.String, Required: true},
		"release":   &hcldec.AttrSpec{Name: "release", Type: cty.String, Required: true},
		"edition":   &hcldec.AttrSpec{Name: "edition", Type: cty.String, Required: true},
		"language":  &hcldec.AttrSpec{Name: "language", Type: cty.String, Required: false},
		"arch":      &hcldec.AttrSpec{Name: "arch", Type: cty.String, Required: false},
		"locale":    &hcldec.AttrSpec{Name: "locale", Type: cty.String, Required: false},
		"cache_ttl": &hcldec.AttrSpec{Name: "cache_ttl", Type: cty.String, Required: false},
	}
}

func (d *Datasource) OutputSpec() hcldec.ObjectSpec {
	return hcldec.ObjectSpec{
		"url":       &hcldec.AttrSpec{Name: "url", Type: cty.String},
		"file_name": &hcldec.AttrSpec{Name: "file_name", Type: cty.String},
	}
}

func (d *Datasource) Configure(configs ...interface{}) error {
	fields := map[string]*string{
		"version":  &d.version,
		"release":  &d.release,
		"edition":  &d.edition,
		"language": &d.language,
		"arch":     &d.arch,
		"locale":   &d.locale,
	}
	cacheTTL := ""
	for _, raw := range configs {
		cval, ok := raw.(cty.Value)
		if !ok || cval.IsNull() || !cval.IsKnown() {
			continue
		}
		for name, ptr := range fields {
			if v := cval.GetAttr(name); v.IsKnown() && !v.IsNull() {
				*ptr = v.AsString()
			}
		}
		if v := cval.GetAttr("cache_ttl"); v.IsKnown() && !v.IsNull() {
			cacheTTL = v.AsString()
		}
	}

	if cacheTTL == "" {
		cacheTTL = defaultCacheTTL
	}
	ttl, err := time.ParseDuration(cacheTTL)
	if err != nil {
		return fmt.Errorf("invalid cache_ttl %q: %w", cacheTTL, err)
	}
	d.cacheTTL = ttl

	return nil
}

func (d *Datasource) Execute() (cty.Value, error) {
	result, err := FetchDownloadURL(Config{
		Version:  d.version,
		Release:  d.release,
		Edition:  d.edition,
		Language: d.language,
		Arch:     d.arch,
		Locale:   d.locale,
		CacheTTL: d.cacheTTL,
	})
	if err != nil {
		return cty.NilVal, err
	}
	return cty.ObjectVal(map[string]cty.Value{
		"url":       cty.StringVal(result.URL),
		"file_name": cty.StringVal(result.FileName),
	}), nil
}
