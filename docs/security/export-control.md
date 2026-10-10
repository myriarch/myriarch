---
title: "Export control"
weight: 30
description: "Assessment of the export-control status of the published software and its cryptography, for the maintainers; not legal advice."
---

## Export control (assessment)

*This is an assessment for the maintainers, based on the rules as published on 2026-10-09. It is not legal advice.
Check your own obligations, in particular before distributing builds that are not publicly available.*

**What the software contains.** myriarch is published as open source under Apache-2.0. It uses only standard,
published cryptography from the Go standard library and widely used open-source libraries: TLS for every network
connection (including the database and the identity provider), SSH for Git (golang.org/x/crypto/ssh), SHA-256 and
HMAC-SHA256, bcrypt, and ECDSA, Ed25519 and RSA signatures for Sigstore verification and OIDC client assertions. It
contains no proprietary or unpublished algorithms or protocols.

**United States (EAR).** Encryption software of this kind could fall under ECCN 5D002. Publicly available encryption
source code classified under 5D002 is not subject to the EAR (15 CFR 742.15(b)(1)). Since the BIS rule of 29 March 2021
(86 FR 16482), the e-mail notification to BIS and the ENC Encryption Request Coordinator is required only for source
code that implements "non-standard cryptography" (15 CFR 742.15(b)(2); definition in 15 CFR 772.1: proprietary or
unpublished cryptographic functionality not adopted by a recognised standards body), which myriarch does not. Published
object code (the release binaries and container images) is not subject to the EAR when it is publicly available and
its source code meets 742.15(b) (15 CFR 734.7(b)). Assessment: neither a classification request nor a notification is
needed for the public repository and the public releases.

**European Union (Regulation (EU) 2021/821).** Annex I, currently as amended by Delegated Regulation (EU) 2025/2003 (in
force since 15 November 2025; the 2026 update adopted on 14 September 2026 applies once it is published in the Official
Journal), does not control software "in the public domain": software made available without restrictions upon its
further dissemination, where copyright restrictions do not change that status (General Software Note, entry b). The
exceptions for Category 5 Part 2 ("Information Security") in that note concern software sold from stock to the general
public (entry a) and minimum object code (entry c), not public-domain software. Assessment: the published source code
and the public releases are not listed dual-use items. The catch-all controls for non-listed items (Articles 4 and 5)
apply only when an exporter is informed by the authorities or aware of a prohibited end use.

**Limits of this assessment.** Sanctions are separate from export classification: direct support, custom builds or
deliveries to persons or destinations under EU or US sanctions need their own check. Builds that are not publicly
available, or a later version with non-standard cryptography, need a new assessment.

Sources:

- [15 CFR 742.15 Encryption items](https://www.law.cornell.edu/cfr/text/15/742.15)
- [15 CFR 734.7 Published](https://www.law.cornell.edu/cfr/text/15/734.7)
- [15 CFR 772.1 Definitions ("non-standard cryptography")](https://www.law.cornell.edu/cfr/text/15/772.1)
- [BIS final rule of 29 March 2021, 86 FR 16482](https://www.govinfo.gov/content/pkg/FR-2021-03-29/html/2021-05481.htm)
- [Regulation (EU) 2021/821, consolidated text of 15 November 2025 (General Software Note, "in the public domain")](https://eur-lex.europa.eu/legal-content/EN/TXT/HTML/?uri=CELEX%3A02021R0821-20251115)
- [Commission Delegated Regulation (EU) 2025/2003](https://eur-lex.europa.eu/eli/reg_del/2025/2003/oj/eng)
- [2026 update of Annex I adopted on 14 September 2026](https://www.nautadutilh.com/en/insights/eu-export-control-2026-update-of-the-dual-use-control-list-annex-i/)
