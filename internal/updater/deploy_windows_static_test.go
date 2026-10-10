// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

package updater

import (
	"regexp"
	"strings"
	"testing"

	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// These checks run without PowerShell (the Windows-specific parts of the
// installer cannot be executed on the build hosts): they pin the security
// properties that must not silently disappear from the script.

func psFunctionBody(t *testing.T, script, name string) string {
	t.Helper()
	i := strings.Index(script, "function "+name+" {")
	require.GreaterOrEqual(t, i, 0, "function %s exists", name)
	rest := script[i:]
	j := strings.Index(rest[1:], "\nfunction ")
	if j < 0 {
		return rest
	}
	return rest[:j+1]
}

func TestInstallWindowsScriptRunsSupervisorUnderVirtualServiceAccount(t *testing.T) {
	script := string(readDeploy(t, "scripts/install-windows.ps1"))
	body := psFunctionBody(t, script, "Install-SupervisorService")
	assert.Regexp(t, `'obj=',\s*\(Get-ServiceAccountName\)`, body, "service account is set with sc.exe config obj=")
	assert.Contains(t, script, `NT SERVICE\$($script:SupervisorService)`)
	assert.NotContains(t, strings.ToLower(script), "localsystem\"", "the supervisor is never configured as LocalSystem")

	grant := psFunctionBody(t, script, "Grant-ServiceAccountAccess")
	assert.Contains(t, grant, "supervisor.yaml")
	assert.Contains(t, grant, "fallback.yaml")
	assert.NotContains(t, grant, "enrollment-token", "the enrollment token stays SYSTEM/Administrators only")
	assert.NotContains(t, grant, "updater.yaml")

	main := psFunctionBody(t, script, "Invoke-Main")
	iSvc := strings.Index(main, "Install-SupervisorService -L $L")
	iGrant := strings.Index(main, "Grant-ServiceAccountAccess")
	iAssert := strings.Index(main, "Assert-TreeAcl")
	iStart := strings.Index(main, "Start-Service -Name $script:SupervisorService")
	require.True(t, iSvc > 0 && iGrant > iSvc && iAssert > iGrant && iStart > iAssert,
		"service created, then granted, then ACLs verified, only then started")
}

func TestInstallWindowsScriptDoesNotAdoptForeignDataDirectories(t *testing.T) {
	script := string(readDeploy(t, "scripts/install-windows.ps1"))
	prot := psFunctionBody(t, script, "Protect-DirectoryTree")
	assert.Contains(t, prot, "SidTrustedInstaller", "owner must be SYSTEM/Administrators/TrustedInstaller")
	assert.Contains(t, prot, "ReparsePoint")
	assert.Contains(t, prot, "SetAccessRuleProtection($true, $false)", "protected DACL")
	assert.Contains(t, prot, "'/reset'", "children are reset")
	assert.Contains(t, prot, "'/setowner'")
	assert.Regexp(t, regexp.MustCompile(`Stop-Install 'directory_unsafe' .*owned by`), prot, "fails closed on a foreign owner")
	init := psFunctionBody(t, script, "Initialize-DataDirectories")
	assert.Contains(t, init, "Protect-DirectoryTree")
	assert.Less(t, strings.Index(init, "Protect-DirectoryTree"), strings.Index(init, "$L.Config"),
		"the root is secured before any subdirectory or file is created")
	assert.NotContains(t, script, "function Set-RestrictedAcl", "the old non-resetting ACL helper is gone")
	main := psFunctionBody(t, script, "Invoke-Main")
	assert.Less(t, strings.Index(main, "Initialize-DataDirectories"), strings.Index(main, "enrollment-token"))
}

func TestInstallWindowsScriptRestrictsRegistryKeyBeforeWritingSecrets(t *testing.T) {
	for _, f := range []string{"scripts/install-windows.ps1", "windows/Set-ServiceEnvironment.ps1"} {
		script := string(readDeploy(t, f))
		body := script
		if strings.HasSuffix(f, "install-windows.ps1") {
			body = psFunctionBody(t, script, "Set-SupervisorEnvironment")
		}
		iAcl := strings.Index(body, "Set-Acl -Path $key")
		iSet := strings.Index(body, "Set-ItemProperty -Path $key -Name Environment")
		require.True(t, iAcl > 0 && iSet > iAcl, "%s: ACL before the secret is written", f)
		assert.NotContains(t, body, "catch", "%s: a failing ACL must abort, not warn", f)
	}
}

func TestInstallWindowsScriptTrustModel(t *testing.T) {
	script := string(readDeploy(t, "scripts/install-windows.ps1"))
	assert.Contains(t, script, "[string]$VerifySignatures = 'require'")
	assert.Contains(t, script, "[switch]$Insecure")
	assert.Contains(t, script, "function Confirm-Signature")
	assert.Contains(t, script, "verify-bundle")
	inputs := psFunctionBody(t, script, "Test-Inputs")
	assert.Contains(t, inputs, "-UpdaterSha256", "the updater checksum is mandatory outside -Insecure")
	assert.Contains(t, inputs, "insecure_required", "plain transport and skipped signatures need -Insecure")
	assert.Regexp(t, `\$VerifySignatures -eq 'skip' -and -not \$Insecure\) \{ Stop-Install 'insecure_required'`, inputs,
		"-VerifySignatures skip is refused without -Insecure")
	dl := psFunctionBody(t, script, "Invoke-Download")
	assert.Contains(t, dl, "Assert-TransportAllowed")
	assert.Regexp(t, `\$final\.Scheme -ne 'https'[\s\S]*Stop-Install 'https_required'`, dl, "redirects to non-https are refused")
	main := psFunctionBody(t, script, "Invoke-Main")
	assert.Less(t, strings.Index(main, "Resolve-Verifier"), strings.Index(main, "Install-Collector -L"))
	for _, fn := range []string{"Install-Collector", "Install-SupervisorBinary"} {
		assert.Contains(t, psFunctionBody(t, script, fn), "Confirm-Signature", fn)
	}
}

// The elevated installer must not trust locations that unprivileged accounts can change.
func TestInstallWindowsScriptDoesNotTrustUserControllableLocations(t *testing.T) {
	script := string(readDeploy(t, "scripts/install-windows.ps1"))

	// the verifier: -Verifier, the SHA-256 checked updater, an installed updater, and only then cosign.exe from an
	// administrator-only directory
	body := psFunctionBody(t, script, "Resolve-Verifier")
	iExplicit := strings.Index(body, "$Verifier)")
	iDownload := strings.Index(body, "$script:UpdaterDownload")
	iInstalled := strings.Index(body, "$installed")
	iCosign := strings.Index(body, "cosign.exe")
	require.True(t, iExplicit >= 0 && iDownload > iExplicit && iInstalled > iDownload && iCosign > iInstalled,
		"verifier order: -Verifier, verified updater, installed updater, cosign.exe")
	assert.Contains(t, body, "Test-AdminOnlyDirectory", "cosign.exe is only accepted from an administrator-only directory")

	// the work directory has a protected DACL before anything is written to it
	work := psFunctionBody(t, script, "New-WorkDirectory")
	assert.Contains(t, work, "SetAccessRuleProtection($true, $false)")
	assert.Contains(t, work, "SetOwner")
	assert.Contains(t, work, "Assert-TreeAcl")
	main := psFunctionBody(t, script, "Invoke-Main")
	assert.Contains(t, main, "New-WorkDirectory")
	assert.Less(t, strings.Index(main, "New-WorkDirectory"), strings.Index(main, "Get-UpdaterDownload"))

	// verified files are hashed again after the copy; a moved staged directory gets the inherited ACL
	assert.Contains(t, psFunctionBody(t, script, "Copy-VerifiedFile"), "checksum_mismatch")
	for _, fn := range []string{"Install-SupervisorBinary", "Enable-CollectorVersion", "Install-UpdaterService"} {
		assert.Contains(t, psFunctionBody(t, script, fn), "Copy-VerifiedFile", fn)
	}
	coll := psFunctionBody(t, script, "Install-Collector")
	assert.Contains(t, coll, "'/reset'")
	assert.Contains(t, coll, "checksum_mismatch")

	// the CA bundle is copied into the protected configuration directory
	assert.Contains(t, psFunctionBody(t, script, "Install-CaFile"), `config\ca.pem`)
	assert.Contains(t, psFunctionBody(t, script, "Grant-ServiceAccountAccess"), "ca.pem")
}
