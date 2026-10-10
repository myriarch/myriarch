#!/usr/bin/env python3
# Copyright The Myriarch Authors
# SPDX-License-Identifier: Apache-2.0

"""Prepare the working copy of the GitOps test data for the e2e run.

usage: prepare_repo.py SRC DST MOCK_PORT

* removes maintenance windows and shortens healthWait/offlineGracePeriod (rollouts finish in seconds)
* points the webhook and AWX executors at the local mock receiver
"""
import pathlib, re, shutil, sys

src, dst, mock_port = pathlib.Path(sys.argv[1]), pathlib.Path(sys.argv[2]), sys.argv[3]
if dst.exists():
    shutil.rmtree(dst)
shutil.copytree(src, dst)

for p in (dst / 'policies').glob('*.yaml'):
    s = p.read_text()
    s = re.sub(r'\n  maintenanceWindows:\n(    .*\n|      .*\n)*', '\n', s)
    s = re.sub(r'healthWait: \S+', 'healthWait: 5s', s)
    s = re.sub(r'offlineGracePeriod: \S+', 'offlineGracePeriod: 2m', s)
    p.write_text(s)

hook = dst / 'executors' / 'k8s-image-bump.yaml'
s = hook.read_text().replace('https://ci-hooks.example.internal/myriarch/k8s-image-bump', f'http://127.0.0.1:{mock_port}/hook')
assert f':{mock_port}/hook' in s
hook.write_text(s)
awx = dst / 'executors' / 'awx-linux.yaml'
s = awx.read_text().replace('https://awx.example.internal', f'http://127.0.0.1:{mock_port}')
assert f':{mock_port}' in s
awx.write_text(s)
