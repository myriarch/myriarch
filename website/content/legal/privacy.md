---
title: "Privacy"
weight: 10
description: "What this website does and does not do with visitor data: no cookies, no analytics and no third-party requests."
---

This page describes the website myriarch.dev. It does not describe the myriarch software, which runs on your own systems.

## What the site does not do

- It sets **no cookies**.
- It runs **no analytics, tracking or advertising** and no A/B tests.
- It makes **no requests to third parties** when a page loads: scripts, styles, fonts, images, the search index and the diagrams are
  all files of this site. There are no fonts, scripts or frames from content delivery networks or other providers.
- It has no forms, no comments and no sign-in.

## What is stored in your browser

If you switch between the light and the dark appearance with the menu in the navigation bar, the browser stores your choice in
`localStorage` under the key `td-color-theme`, so the next page opens in the same appearance. It stays on your device, is never
sent anywhere and is not used to recognize you. Without a choice the site follows the appearance setting of your system. Clearing the
site data of your browser removes it. The search runs in your browser against a file of this site; the queries are not sent anywhere.

## Server logs

The site is a set of static files that a hosting provider delivers. Like every web server, the host receives the address of your
device, the time, the requested path, the browser identification and the referring page with each request, and may keep this in
access logs for operating the service and defending against abuse. The project does not receive these logs and does not use
them to identify visitors. Which provider hosts the site, and how long it keeps logs, is stated in the repository documentation
once the site is published.

## Links to other sites

Links to other sites, for example to the repository on GitHub, leave this site. The providers of those sites have their own
privacy policies.

## Changes

If the site ever adds statistics, only a cookieless solution that does not identify visitors will be considered, and only after a
public decision record ([ADR-0031](/docs/adr/0031-website-with-hugo-and-docsy/)). The history of this page is the history of the
[repository](https://github.com/myriarch/myriarch).
