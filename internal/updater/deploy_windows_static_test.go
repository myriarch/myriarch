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

	// the verifier: -Verifier, the SHA-256 checked updater (only with -UpdaterSha256 and without -UpdaterChecksumsUrl), an
	// installed updater, and only then cosign.exe from an administrator-only directory
	body := psFunctionBody(t, script, "Resolve-Verifier")
	iExplicit := strings.Index(body, "$Verifier)")
	iDownload := strings.Index(body, "$script:UpdaterDownload")
	iInstalled := strings.Index(body, "$installed")
	iCosign := strings.Index(body, "cosign.exe")
	require.True(t, iExplicit >= 0 && iDownload > iExplicit && iInstalled > iDownload && iCosign > iInstalled,
		"verifier order: -Verifier, verified updater, installed updater, cosign.exe")
	assert.Contains(t, body, "Test-AdminOnlyDirectory", "cosign.exe is only accepted from an administrator-only directory")
	// an explicit -Verifier is subject to the same directory check, before its version is read or it is run
	assert.Regexp(t, `(?s)if \(\$Verifier\) \{.*?Test-AclEnforced.*?Test-AdminOnlyDirectory.*?Stop-Install 'directory_unsafe'.*?Get-CosignVersionProblem`, body,
		"-Verifier must be in an administrator-only directory (an elevated process must not run a replaceable binary)")
	assert.Contains(t, script, "[Environment]::OSVersion.Platform -eq [PlatformID]::Win32NT", "the ACL check is enforced on Windows")
	assert.Regexp(t, `\$script:UpdaterDownload -and -not \$UpdaterChecksumsUrl -and \$UpdaterSha256`, body,
		"the downloaded updater verifies only when its hash does not come from the file that the verifier has to check")
	assert.Contains(t, body, "Get-CosignVersionProblem", "a cosign must be 3.0.4 or newer")
	assert.Contains(t, script, "$script:MinCosignVersion = [Version]'3.0.4'")

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

// psUsesDotNet matches what a restricted language mode refuses in PowerShell code: a static member (::), a method call
// (.Name( ) and a cmdlet that creates objects of any type.
var psUsesDotNet = regexp.MustCompile(`::|\.[A-Za-z]+\(|New-Object|Add-Type`)

// psWithoutComments drops the lines of a function body that are comments.
func psWithoutComments(body string) string {
	var kept []string
	for _, l := range strings.Split(body, "\n") {
		if !strings.HasPrefix(strings.TrimSpace(l), "#") {
			kept = append(kept, l)
		}
	}
	return strings.Join(kept, "\n")
}

// The language mode is the first action of Invoke-Main, before the first .NET method call, which App Control for Business
// makes fail with an error that does not name the cause. The check, the error that it raises and the way the error is printed
// use properties, cmdlets and a throw only; a method call on the host is replaced by Write-Host in the restricted modes.
func TestInstallWindowsScriptChecksTheLanguageModeBeforeAnythingElse(t *testing.T) {
	script := string(readDeploy(t, "scripts/install-windows.ps1"))
	main := psFunctionBody(t, script, "Invoke-Main")
	var statements []string
	for _, l := range strings.Split(main, "\n")[1:] {
		if l = strings.TrimSpace(l); l != "" && !strings.HasPrefix(l, "#") {
			statements = append(statements, l)
		}
	}
	require.NotEmpty(t, statements)
	assert.Equal(t, "Assert-FullLanguageMode", statements[0], "the language mode check is the first action of Invoke-Main")
	iFirstMethod := psUsesDotNet.FindStringIndex(main)
	require.NotNil(t, iFirstMethod, "Invoke-Main calls methods")
	assert.Less(t, strings.Index(main, "Assert-FullLanguageMode"), iFirstMethod[0], "the check comes before the first .NET use")

	check := psFunctionBody(t, script, "Assert-FullLanguageMode")
	assert.Contains(t, check, "$ExecutionContext.SessionState.LanguageMode")
	assert.Contains(t, check, "Stop-Install 'language_mode_unsupported'")
	assert.NotRegexp(t, psUsesDotNet, psWithoutComments(check), "the check itself must work in Constrained Language Mode")
	stop := strings.SplitN(psFunctionBody(t, script, "Stop-Install"), "\n", 2)[0] // a one-line function
	assert.NotRegexp(t, psUsesDotNet, stop, "the error that it raises must work in Constrained Language Mode")

	// the top-level handler: the method call on the host stays in FullLanguage, the other modes print with Write-Host
	tail := script[strings.Index(script, "# Do not run when the script is dot-sourced"):]
	iMode := strings.Index(tail, "$ExecutionContext.SessionState.LanguageMode -eq 'FullLanguage'")
	iHost := strings.Index(tail, `$Host.UI.WriteErrorLine("[myriarch-install] ERROR $msg")`)
	iWrite := strings.Index(tail, `Write-Host "[myriarch-install] ERROR $msg"`)
	iExit := strings.Index(tail, "exit 1")
	require.True(t, iMode > 0 && iHost > iMode && iWrite > iHost && iExit > iWrite,
		"FullLanguage: the host prints the error; any other mode: Write-Host does; then exit 1")
}

// With a checksums.txt the verifier exists before the updater is downloaded, the file is checked through the same
// function as the upstream bundles, and the removed key options fail before anything else happens.
func TestInstallWindowsScriptKeylessChecksumsFlow(t *testing.T) {
	script := string(readDeploy(t, "scripts/install-windows.ps1"))
	main := psFunctionBody(t, script, "Invoke-Main")
	iVerifier := strings.Index(main, "Resolve-Verifier -L $L")
	iDownload := strings.Index(main, "Get-UpdaterDownload -TmpDir $tmpDir")
	require.True(t, iVerifier > 0 && iDownload > iVerifier, "with -UpdaterChecksumsUrl the verifier is resolved first")
	assert.Contains(t, main, "if ($WithUpdater -and $UpdaterChecksumsUrl) {")
	iRemoved := strings.Index(main, "'option_removed'")
	iPriv := strings.Index(main, "'privilege_required'")
	require.True(t, iRemoved > 0 && iPriv > iRemoved, "removed options are reported before the privilege check")

	sums := psFunctionBody(t, script, "Get-UpdaterSha256FromChecksums")
	assert.Contains(t, sums, "Confirm-Signature")
	assert.Contains(t, sums, "Get-ReleaseCertIdentity -Version $script:UpdaterResolvedVersion")
	assert.Less(t, strings.Index(sums, "Confirm-Signature"), strings.Index(sums, "Get-ChecksumFromFile"), "the hash is read after the bundle is verified")
	identity := psFunctionBody(t, script, "Get-ReleaseCertIdentity")
	assert.Contains(t, identity, `[regex]::Escape($Version) + '$'`, "the tag is escaped and the pattern is anchored")
	assert.Contains(t, identity, `release\.yml@refs/tags/v`)

	inputs := psFunctionBody(t, script, "Test-Inputs")
	assert.Contains(t, inputs, `"$UpdaterChecksumsUrl.sigstore.json"`, "the bundle defaults to <checksums-url>.sigstore.json")
	assert.Contains(t, inputs, "-VerifySignatures skip: checksums.txt would not be authenticated")

	// the key-based checks are gone; the parameters remain only to produce option_removed
	for _, gone := range []string{"Confirm-BlobSignature", "New-EcdsaVerifier", "ConvertFrom-DerEcdsaSignature", "signing_key_invalid", "signature_invalid", "signature_malformed"} {
		assert.NotContains(t, script, gone)
	}
	assert.Contains(t, script, "-UpdaterSignerThumbprint", "the Authenticode pin stays for private PKIs")
}
