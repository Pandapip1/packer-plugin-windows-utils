# packer-plugin-windows-utils

A [Packer](https://www.packer.io/) plugin providing datasources for working
with Windows install media.

## Datasources

### `windows-utils-patch`

Strips the "Press any key to boot from CD or DVD..." EFI boot prompt from a
Windows ISO (so it boots unattended), optionally injecting extra drivers
into a `$WinPeDriver$` folder for WinPE/Setup to auto-discover.

```hcl
data "windows-utils-patch" "win11" {
  iso_path      = "/path/to/original.iso"
  patch_noboot  = true
  extra_drivers = ["/path/to/driver/vioscsi.inf"]
}

source "qemu" "example" {
  iso_url = data.windows-utils-patch.win11.patched_iso_path
  # ...
}
```

### `windows-utils-windows-iso`

Resolves an official Microsoft Windows ISO download URL (a Go
reimplementation of the [Fido](https://github.com/pbatard/Fido) script).

```hcl
data "windows-utils-windows-iso" "win11" {
  version  = "Windows 11"
  release  = "24H2"
  edition  = "Pro"
  language = "English International"
  arch     = "x64"
}
```

### `windows-utils-debloat`

Debloats a Windows install image the same way the
[nano11-go](https://github.com/Pandapip1/nano11-go) CLI does -- AppX/
servicing-package/WinSxS/file-cleanup/registry-tweak/service removal against
`install.wim`, entirely offline (no DISM, no mounted image, no admin
rights) -- and authors a new bootable ISO from the result. It optionally
bakes in caller-supplied registry tweaks before the ISO is authored.

This datasource shells out to a `nano11-go` binary for the actual debloat and
ISO-authoring work (nano11-go's removal logic lives entirely in its own
`package main`, so it is not importable as a Go library without forking it);
`nano11-go` must be built and either on `PATH` or pointed to via
`nano11go_binary`. `registry_tweaks`, however, is applied directly via the
[gowim](https://github.com/Pandapip1/gowim) `regf`/`registry` Go packages --
no subprocess, no nano11-go involvement -- since that step is cleanly
separable library code.

```hcl
data "windows-utils-debloat" "win11" {
  iso_path = data.windows-utils-windows-iso.win11.file_name

  # All of the following are optional; leaving them unset reproduces
  # nano11-go's own CLI defaults exactly.
  skip_appx             = false
  skip_packages         = false
  skip_file_cleanup     = false
  skip_winsxs_wipe      = false
  skip_reg_tweaks       = false
  skip_services         = false
  keep_nic_drivers      = true  # keep vendor NIC drivers for physical hardware
  keep_drivers          = false
  keep_defender_search  = false
  remove_web_engines    = false
  remove_ai             = false
  remove_store_apps     = false
  remove_uwp_frameworks = false
  remove_ai_foundation  = false
  remove_ime            = false
  keep_iso_extras       = false
  skip_iso_autounattend = false

  image_index     = 1       # 1-based edition index within install.wim
  lzx_preset      = "fast"  # fast | balanced | default | max | none
  iso_volume_id   = "Nano11Go"
  nano11go_binary = "nano11-go" # path to the built nano11-go binary

  # Baked in offline via gowim's regf/registry packages, before the ISO is
  # authored -- e.g. this is exactly how you'd pre-bake
  # LocalAccountTokenFilterPolicy=1 instead of doing it at runtime via an
  # Autounattend.xml specialize-pass reg.exe invocation.
  registry_tweaks = [
    {
      hive  = "SOFTWARE"
      path  = "Microsoft\\Windows\\CurrentVersion\\Policies\\System"
      name  = "LocalAccountTokenFilterPolicy"
      type  = "dword"
      value = "1"
    },
  ]
}

source "qemu" "example" {
  iso_url = data.windows-utils-debloat.win11.debloated_iso_path
  # ...
}
```

`registry_tweaks` entries support `hive` values `SYSTEM`, `SOFTWARE`,
`DEFAULT`, `SAM`, `COMPONENTS`, or `NTUSER.DAT` (`Users\Default\NTUSER.DAT`),
and `type` values `dword` (decimal or `0x`-prefixed hex) or `string`.

Not currently exposed: nano11-go's `boot.wim` shrink/tweak stage and its
GRUB dual-boot ISO authoring options -- out of scope for this first version.
