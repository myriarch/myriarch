#!/usr/bin/env bash
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

# Language guard for tracked text files: code comments and log messages are English (ADR-0020).
# Flags lines that contain German umlauts or at least N distinct German stop words. It prints only
# location, category and the matched stop words, never the text of the line.
#
# Usage: scripts/check-language.sh [options] [--] [pathspec...]   (see --help)
# Exit:  0 = no findings (or --warn), 1 = findings, 2 = usage or environment error.
set -euo pipefail

SCRIPT_DIR="$(cd "$(dirname "${BASH_SOURCE[0]}")" && pwd)"

usage() {
  cat <<'USAGE'
Usage: scripts/check-language.sh [options] [--] [pathspec...]

Reports lines with German umlauts or German stop words in tracked text files.

  pathspec...           git pathspecs relative to the repository root (default: whole repository)
  -C DIR                repository to scan (default: the repository containing this script)
  --exclude-file FILE   file with one exclude glob per line (default: scripts/check-language.exclude);
                        /dev/null means no default excludes
  --exclude GLOB        additional exclude glob, repeatable
  --untracked           also scan untracked files that are not ignored
  --warn                report only, exit 0 even with findings
  --summary-only        print only the summary and the finding count per top-level directory
  --min-words N         distinct stop words per line needed for a finding (default: 2)
  -h, --help            show this help

A line containing "check-language: ignore" is skipped. Exit codes: 0 clean, 1 findings, 2 error.
USAGE
}

usage_error() { printf 'check-language: %s\n' "$1" >&2; usage >&2; exit 2; }
env_error()   { printf 'check-language: %s\n' "$1" >&2; exit 2; }

# Perl program: reads NUL-separated paths from stdin, scans the work-tree content of each file.
# Arguments: repository root, min words, warn (0/1), summary only (0/1).
read -r -d '' PERL_PROGRAM <<'PERL' || true
use strict;
use warnings;
use Encode ();

my ($root, $min_words, $warn, $summary_only) = @ARGV;
chdir $root or do { print STDERR "check-language: cannot enter $root\n"; exit 2 };

# Fixed German stop word list (lower case; the capitalized form of every word is added below). Deliberately without
# words that are also common English or code identifiers: die, also, was, will, war, an, in, so, man, hat, am, um,
# im, es, er, Standard.
my @words = qw(
  und oder nicht ist sind wird werden wurde wurden mit fuer ueber auch noch nur wenn dann sonst
  bei beim zum zur vom eine einen einem einer eines ein der das dem den des dass sich kein keine
  ohne aus nach wie schon hier siehe bitte muss kann darf soll bereits fehlt jetzt gibt sowie
  damit weil auf zu wir sie koennen muessen moeglich spaeter naechste aendern pruefen
  von unter durch alle diese dieser dieses diesen jede jeder jedes sein haben aber mehr
  Fehler Datei Verzeichnis Aufruf Werkzeug Werkzeuge Pruefung Hinweis Achtung
);
my %seen;
my @unique = grep { !$seen{$_}++ } map { ($_, ucfirst $_) } @words;
my $alt = join '|', sort { length($b) <=> length($a) || $a cmp $b } @unique;
# A word boundary: the neighbors are no letters (umlauts included), digits, "_" or "-".
my $stop_re = qr/(?<![\p{L}\p{N}_-])(?:$alt)(?![\p{L}\p{N}_-])/;
my $umlaut_re = qr/[\x{e4}\x{f6}\x{fc}\x{c4}\x{d6}\x{dc}\x{df}]/;

binmode STDIN;
local $/ = "\0";
my @paths = <STDIN>;
chomp @paths;

my (@findings, %files_with, $scanned, $skipped, $n_umlaut, $n_stop);
$scanned = $skipped = $n_umlaut = $n_stop = 0;

for my $path (@paths) {
  next if $path eq '';
  next if -l $path || !-f $path;
  my $bytes = do {
    open my $fh, '<:raw', $path or next;
    local $/;
    <$fh>;
  };
  next unless defined $bytes;
  if (substr($bytes, 0, 8000) =~ /\0/) { $skipped++; next }
  my $text = eval { Encode::decode('UTF-8', $bytes, Encode::FB_CROAK() | Encode::LEAVE_SRC()) };
  if (!defined $text) { $skipped++; next }
  $scanned++;
  my $n = 0;
  for my $line (split /\n/, $text) {
    $n++;
    next if index($line, 'check-language: ignore') >= 0;
    if ($line =~ $umlaut_re) {
      push @findings, [$path, $n, 'umlaut'];
      $n_umlaut++;
      $files_with{$path} = 1;
      next;
    }
    (my $plain = $line) =~ s{[a-z]+://\S+}{ }g;
    my (%distinct, @found);
    while ($plain =~ /($stop_re)/g) {
      push @found, $1 unless $distinct{lc $1}++;   # distinct ignoring case
    }
    if (@found >= $min_words) {
      push @findings, [$path, $n, 'stopwords (' . join(', ', @found) . ')'];
      $n_stop++;
      $files_with{$path} = 1;
    }
  }
}

@findings = sort { $a->[0] cmp $b->[0] || $a->[1] <=> $b->[1] } @findings;
binmode STDOUT;
unless ($summary_only) {
  print "$_->[0]:$_->[1]: $_->[2]\n" for @findings;
}
if ($summary_only) {
  my %per_dir;
  for my $f (@findings) {
    my $dir = $f->[0] =~ m{^([^/]+)/} ? $1 : '.';
    $per_dir{$dir}++;
  }
  print "  $_/: $per_dir{$_}\n" for sort { $per_dir{$b} <=> $per_dir{$a} || $a cmp $b } keys %per_dir;
}
printf "check-language: %d finding(s) in %d file(s) [umlaut: %d, stopwords: %d]; scanned %d file(s), skipped %d\n",
  scalar(@findings), scalar(keys %files_with), $n_umlaut, $n_stop, $scanned, $skipped;
exit(@findings && !$warn ? 1 : 0);
PERL

main() {
  local repo="${SCRIPT_DIR}/.." exclude_file="${SCRIPT_DIR}/check-language.exclude"
  local untracked=0 warn=0 summary_only=0 min_words=2
  local -a excludes=() pathspecs=()

  while [ $# -gt 0 ]; do
    case "$1" in
      -h|--help)      usage; exit 0 ;;
      -C)             [ $# -ge 2 ] || usage_error "option -C needs a directory"; repo="$2"; shift 2 ;;
      --exclude-file) [ $# -ge 2 ] || usage_error "option --exclude-file needs a file"; exclude_file="$2"; shift 2 ;;
      --exclude)      [ $# -ge 2 ] || usage_error "option --exclude needs a glob"; excludes+=("$2"); shift 2 ;;
      --min-words)    [ $# -ge 2 ] || usage_error "option --min-words needs a number"; min_words="$2"; shift 2 ;;
      --untracked)    untracked=1; shift ;;
      --warn)         warn=1; shift ;;
      --summary-only) summary_only=1; shift ;;
      --)             shift; pathspecs+=("$@"); break ;;
      -*)             usage_error "unknown option: $1" ;;
      *)              pathspecs+=("$1"); shift ;;
    esac
  done
  case "${min_words}" in ''|*[!0-9]*|0) usage_error "--min-words needs a positive integer" ;; esac

  command -v git >/dev/null 2>&1 || env_error "git not found"
  command -v perl >/dev/null 2>&1 || env_error "perl not found"
  local root
  root="$(git -C "${repo}" rev-parse --show-toplevel 2>/dev/null)" || env_error "not a git work tree: ${repo}"
  [ -r "${exclude_file}" ] && [ -f "${exclude_file}" ] || [ "${exclude_file}" = /dev/null ] \
    || env_error "cannot read exclude file: ${exclude_file}"

  local line
  while IFS= read -r line || [ -n "${line}" ]; do
    case "${line}" in ''|'#'*) continue ;; esac
    excludes+=("${line}")
  done < "${exclude_file}"

  local -a ls_args=(ls-files -z)
  [ "${untracked}" = 1 ] && ls_args+=(-c -o --exclude-standard)
  ls_args+=(--)
  # Excludes go first: git 2.52 lists no files for some nested pathspecs when an exclude pathspec follows them.
  local glob
  for glob in ${excludes[@]+"${excludes[@]}"}; do ls_args+=(":(exclude,glob)${glob}"); done
  [ "${#pathspecs[@]}" -gt 0 ] && ls_args+=("${pathspecs[@]}")

  local list
  list="$(mktemp "${TMPDIR:-/tmp}/check-language.XXXXXX")"
  trap 'rm -f "${list}"' EXIT
  git -C "${root}" "${ls_args[@]}" > "${list}" || env_error "git ls-files failed in ${root}"

  local rc=0
  perl -e "${PERL_PROGRAM}" -- "${root}" "${min_words}" "${warn}" "${summary_only}" < "${list}" || rc=$?
  exit "${rc}"
}

main "$@"
