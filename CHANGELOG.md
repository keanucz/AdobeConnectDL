# Changelog

Notable changes to AdobeConnectDL. Full release notes for every version are on
the [releases page](https://github.com/keanucz/adobeconnectdl/releases).

## v2.3.0 - 2026-10-11

### Bug Fixes

- Attachments are downloaded again for recordings that share files through the
  resource pod (`ftresource*.xml`). Previously only the older file share pod
  (`ftfileshare*.xml`) was read, so these recordings saved no documents.

### New Features

- Web links shared in a session are written to `links.txt` next to
  `documents.txt`.
