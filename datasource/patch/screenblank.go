package patch

import "github.com/Pandapip1/gowim/registry"

// screenSaverOffTimeoutGUID is the well-documented "Turn off display after"
// power setting (PowerCfg alias VIDEOIDLE) under the Display subgroup
// (7516b95f-f776-4464-8c53-06167f40cc99). Confirmed against Microsoft's own
// Power.admx Group Policy template (e.g.
// https://github.com/nsacyber/Windows-Secure-Host-Baseline/blob/master/Windows/Group%20Policy%20Templates/Power.admx,
// policies VideoPowerDownTimeOutAC_2/VideoPowerDownTimeOutDC_2): both are
// class="Machine" (HKLM) policies keyed at
// Software\Policies\Microsoft\Power\PowerSettings\<this GUID> with decimal
// elements named ACSettingIndex/DCSettingIndex -- i.e. the registry path is
// flat (PowerSettings\<setting-guid>), not nested under the subgroup GUID.
// These Policies-tree values are read directly by the power service itself
// (this is the same "ADMX-backed policy overrides the live setting"
// mechanism used throughout Administrative Templates, not the separate
// Group-Policy-Preferences client-side extension that copies values into the
// active PowerSchemes tree) and take effect on the next boot/logon with no
// gpupdate needed -- see
// https://learn.microsoft.com/en-us/answers/questions/1192075/what-is-the-purpose-of-provacsettingindex-provdcse
// and the ADMX_Power Policy CSP reference
// (https://learn.microsoft.com/en-us/windows/client-management/mdm/policy-csp-admx-power)
// for the same GUID/valueName pairing on neighboring PowerSettings policies.
const screenSaverOffTimeoutGUID = `3C0BC021-C8A8-4E07-A973-6B14CBCB2B7E`

// screenBlankRegistryTweaks returns the fixed registry_tweaks equivalent of
// "never let the screen go blank": disabling the screensaver and disabling
// the display/monitor power-off timeout, baked in offline so a
// screenshot-driven VNC/serial console session never needs a keypress to
// wake the display.
//
// Screensaver: HKCU\Control Panel\Desktop\ScreenSaveActive/ScreenSaveTimeOut
// is the setting real Windows reads (confirmed via the Group Policy
// Desktop.admx/ControlPanelDisplay.admx source -- e.g.
// https://github.com/mxk/win10-secure-baseline-gpo/blob/master/PolicyDefinitions/ControlPanelDisplay.admx --
// policies CPL_Personalization_EnableScreenSaver/CPL_Personalization_ScreenSaverTimeOut
// are both declared class="User", with NO class="Machine"/"Both" variant,
// meaning there is no supported HKLM-wide policy override for this setting
// the way there is for the power/display-off timeout below -- writing it
// under HKLM\SOFTWARE\Policies\Microsoft\Windows\Control Panel\Desktop is
// not a documented enforcement point). Since this datasource patches an
// image that has never booted, no HKEY_CURRENT_USER exists yet for the
// local administrator account Autounattend.xml's <LocalAccounts> creates
// (see autounattend.xml.pkrtpl.hcl, read for context only). Windows instead
// seeds every brand-new local profile's HKCU from
// C:\Users\Default\NTUSER.DAT at first logon -- long-documented,
// current behavior (see
// https://learn.microsoft.com/en-us/windows-hardware/manufacture/desktop/customize-the-default-user-profile-by-using-copyprofile
// and https://learn.microsoft.com/en-US/troubleshoot/windows-client/deployment/customize-default-local-user-profile:
// "the Default User profile is created, and the first time that a user logs
// on, the Default User profile is copied to the user's profile"). That is
// exactly gowim/registry's HiveDefaultUser (Users\Default\NTUSER.DAT), so
// writing the setting there lands in HKCU\Control Panel\Desktop for the
// admin account the moment its profile is created -- which, combined with
// this pipeline's <AutoLogon>, happens automatically on first boot with no
// further scripting.
//
// Display/monitor power-off: HKLM\SOFTWARE\Policies\Microsoft\Power\
// PowerSettings\<screenSaverOffTimeoutGUID>!ACSettingIndex/DCSettingIndex
// (see screenSaverOffTimeoutGUID's doc comment) -- a real class="Machine"
// Group Policy, so it applies regardless of which power scheme GUID ends up
// active at first boot (unlike trying to bake a value directly into
// HKLM\SYSTEM\...\PowerSchemes\<scheme-guid>\..., which would be fragile
// offline since the live scheme GUID isn't guaranteed stable across Windows
// versions/images). 0 means "never" for both AC and DC, matching normal
// powercfg semantics for this setting.
func screenBlankRegistryTweaks() []RegistryTweak {
	return []RegistryTweak{
		{
			Hive: registry.HiveDefaultUser, Path: `Control Panel\Desktop`, Name: "ScreenSaveActive",
			Type: "string", Value: "0",
		},
		{
			Hive: registry.HiveDefaultUser, Path: `Control Panel\Desktop`, Name: "ScreenSaveTimeOut",
			Type: "string", Value: "0",
		},
		{
			Hive: registry.HiveSoftware, Path: `Policies\Microsoft\Power\PowerSettings\` + screenSaverOffTimeoutGUID, Name: "ACSettingIndex",
			Type: "dword", Value: "0",
		},
		{
			Hive: registry.HiveSoftware, Path: `Policies\Microsoft\Power\PowerSettings\` + screenSaverOffTimeoutGUID, Name: "DCSettingIndex",
			Type: "dword", Value: "0",
		},
	}
}
