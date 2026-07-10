package main

import (
	"fmt"
	"os"

	"github.com/hashicorp/packer-plugin-sdk/plugin"

	"github.com/Pandapip1/packer-plugin-windows-utils/datasource/patch_noprompt"
	"github.com/Pandapip1/packer-plugin-windows-utils/version"
)

func main() {
	pps := plugin.NewSet()
	pps.RegisterDatasource("patch-noprompt", new(patch_noprompt.Datasource))
	pps.SetVersion(version.PluginVersion)
	err := pps.Run()
	if err != nil {
		fmt.Fprintln(os.Stderr, err.Error())
		os.Exit(1)
	}
}
