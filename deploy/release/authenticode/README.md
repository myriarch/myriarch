# Trust anchors for Authenticode

Stage apply of the release build ([`../README.md`](../README.md)) accepts a signed Windows file only when it verifies against the
files in this directory. The pull request that connects a signing provider commits three files here:

| File | Content |
|---|---|
| `ca.pem` | PEM: the roots and intermediates of the code-signing certificate chain |
| `tsa-ca.pem` | PEM: the roots of the timestamp authorities (RFC 3161) |
| `subject.txt` | One line: the exact subject of the signing certificate in RFC 2253 form, as `osslsigncode verify` prints it |

Each file needs an entry in `REUSE.toml` with attribution, because a certificate is third-party data.

Until the files exist, stage apply accepts only unsigned files (`signer.json` with `signed: false`) and fails closed with
`[trust_missing]` on signed ones. Only snapshot builds can point `MYRIARCH_AUTHENTICODE_DIR` at another directory (the rehearsal does
this with a throwaway test CA); a release build always reads this directory. The decision record for the Authenticode path
(ADR-0034) explains the choice of provider and what is verified.
