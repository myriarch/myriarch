#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Throwaway test CA for the tests and the rehearsal of the Windows signing hook. It is NEVER used for a published file: the keys
# are created in a directory outside the repository, with a validity of seven days, and are deleted by the caller.
#
#   ci/release/authenticode-test-ca.sh init <dir> [subject-cn]       roots, a code-signing signer and a timestamp signer
#   ci/release/authenticode-test-ca.sh leaf <dir> <subject-cn> <out-dir>   another signer from the root of <dir>
#
# init writes
#   <dir>/ca/{root,tsa-root}.{key,pem}          the two roots (code signing and timestamping)
#   <dir>/signing/{signer,tsa}.{key,pem}        the signer (CN from the argument) and the timestamp signer
#   <dir>/trust/{ca.pem,tsa-ca.pem,subject.txt} what stage apply trusts (deploy/release/authenticode/README.md)
# leaf writes <out-dir>/signing/{signer.*,tsa.*} and <out-dir>/trust/ (the trust files of <dir>, with the new subject).
#
# OpenSSL 3 is required. There are no CRL distribution points in the certificates, so verification needs no network.
set -euo pipefail
# shellcheck source=ci/lib.sh
. "$(dirname "${BASH_SOURCE[0]}")/../lib.sh"
umask 077
case "$(openssl version 2>/dev/null)" in
  "OpenSSL 3"*) ;;
  *) die "[openssl_version] OpenSSL 3 is required (openssl version: $(openssl version 2>/dev/null || echo none))" ;;
esac

ORG="Myriarch Test"
BITS=3072
DAYS=7

refuse_inside_repo() { # dir
  local d abs
  mkdir -p "$1"
  abs="$(cd "$1" && pwd -P)"
  case "${abs}/" in
    "$(cd "${ROOT}" && pwd -P)/"*) die "[inside_repository] ${abs} is inside the repository: the test CA keys must live outside of it" ;;
  esac
}

# new_root <dir> <name> <cn>
new_root() {
  openssl req -x509 -new -newkey "rsa:${BITS}" -nodes -days "${DAYS}" -sha256 \
    -keyout "$1/$2.key" -out "$1/$2.pem" -subj "/O=${ORG}/CN=$3" \
    -addext "basicConstraints=critical,CA:TRUE" -addext "keyUsage=critical,keyCertSign,cRLSign" \
    -addext "subjectKeyIdentifier=hash" 2>/dev/null
}

# new_leaf <ca-dir> <out-dir> <name> <cn> <root-name> <extension-lines>
new_leaf() {
  local cad="$1" out="$2" name="$3" cn="$4" root="$5" ext="$6" tmp
  tmp="$(mktemp -d)"
  printf '%s\n' "[ext]" "${ext}" > "${tmp}/ext.cnf"
  openssl req -new -newkey "rsa:${BITS}" -nodes -keyout "${out}/${name}.key" -out "${tmp}/${name}.csr" \
    -subj "/O=${ORG}/CN=${cn}" 2>/dev/null
  openssl x509 -req -in "${tmp}/${name}.csr" -CA "${cad}/${root}.pem" -CAkey "${cad}/${root}.key" -set_serial "0x$(openssl rand -hex 16)" \
    -days "${DAYS}" -sha256 -extfile "${tmp}/ext.cnf" -extensions ext -out "${out}/${name}.pem" 2>/dev/null
  rm -rf "${tmp}"
}

SIGNER_EXT='basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=codeSigning
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid'
TSA_EXT='basicConstraints=critical,CA:FALSE
keyUsage=critical,digitalSignature
extendedKeyUsage=critical,timeStamping
subjectKeyIdentifier=hash
authorityKeyIdentifier=keyid'

write_subject() { # signer.pem out
  openssl x509 -in "$1" -noout -subject -nameopt RFC2253 | sed 's/^subject=[[:space:]]*//' > "$2"
}

cmd="${1:-}"
case "${cmd}" in
  init)
    [ "$#" -ge 2 ] && [ "$#" -le 3 ] || die "usage: authenticode-test-ca.sh init <dir> [subject-cn]"
    dir="$2"; cn="${3:-Myriarch Test Signer}"
    refuse_inside_repo "${dir}"
    mkdir -p "${dir}/ca" "${dir}/signing" "${dir}/trust"
    new_root "${dir}/ca" root "Myriarch Test Code Signing Root"
    new_root "${dir}/ca" tsa-root "Myriarch Test Timestamp Root"
    new_leaf "${dir}/ca" "${dir}/signing" signer "${cn}" root "${SIGNER_EXT}"
    new_leaf "${dir}/ca" "${dir}/signing" tsa "Myriarch Test TSA" tsa-root "${TSA_EXT}"
    cp "${dir}/ca/root.pem" "${dir}/trust/ca.pem"
    cp "${dir}/ca/tsa-root.pem" "${dir}/trust/tsa-ca.pem"
    write_subject "${dir}/signing/signer.pem" "${dir}/trust/subject.txt"
    ;;
  leaf)
    [ "$#" = 4 ] || die "usage: authenticode-test-ca.sh leaf <dir> <subject-cn> <out-dir>"
    dir="$2"; cn="$3"; out="$4"
    [ -f "${dir}/ca/root.key" ] || die "${dir} holds no test CA (authenticode-test-ca.sh init)"
    refuse_inside_repo "${out}"
    mkdir -p "${out}/signing" "${out}/trust"
    new_leaf "${dir}/ca" "${out}/signing" signer "${cn}" root "${SIGNER_EXT}"
    cp "${dir}/signing/tsa.key" "${dir}/signing/tsa.pem" "${out}/signing/"
    cp "${dir}/trust/ca.pem" "${dir}/trust/tsa-ca.pem" "${out}/trust/"
    write_subject "${out}/signing/signer.pem" "${out}/trust/subject.txt"
    ;;
  *) die "usage: authenticode-test-ca.sh init <dir> [subject-cn] | leaf <dir> <subject-cn> <out-dir>" ;;
esac
