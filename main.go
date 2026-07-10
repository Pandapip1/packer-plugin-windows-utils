package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/packer-plugin-sdk/plugin"

	"github.com/Pandapip1/packer-plugin-windows-utils/datasource/patch"
	"github.com/Pandapip1/packer-plugin-windows-utils/datasource/windows_iso"
	"github.com/Pandapip1/packer-plugin-windows-utils/version"
)

func main() {
	pps := plugin.NewSet()
	pps.RegisterDatasource("patch", new(patch.Datasource))
	pps.RegisterDatasource("windows-iso", new(windows_iso.Datasource))
	pps.SetVersion(version.PluginVersion)
	err := pps.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
