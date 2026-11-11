# Changelog

## [1.0.0](https://github.com/vitalvas/nat-pmp/compare/v0.4.0...v1.0.0) (2026-11-10)


### Features

* migrate module path to go.vitalvas.com/nat-pmp ([e4e0518](https://github.com/vitalvas/nat-pmp/commit/e4e0518e4b2d990c8b8055172bede2e81d23cd90))
* replace cobra with gokit/xflags on new gokit module path ([c9100d2](https://github.com/vitalvas/nat-pmp/commit/c9100d267fe657cf019f6c58ea0f4a7b8986f010))


### Bug Fixes

* harden port mapping protocols ([6af0d25](https://github.com/vitalvas/nat-pmp/commit/6af0d25fd06107ac718a483f60ff08a6af00088d))


### Miscellaneous Chores

* release 1.0.0 ([9eb5ff1](https://github.com/vitalvas/nat-pmp/commit/9eb5ff1e68f7238313c9185cae44ac98b6cc0439))

## [0.4.0](https://github.com/vitalvas/nat-pmp/compare/v0.3.1...v0.4.0) (2026-07-13)


### Features

* refresh internal address on renewal and lower default lease ([9258b97](https://github.com/vitalvas/nat-pmp/commit/9258b973d4ef69d19734f061219d85421e496709))

## [0.3.1](https://github.com/vitalvas/nat-pmp/compare/v0.3.0...v0.3.1) (2026-07-08)


### Bug Fixes

* log internal address on active mappings ([429eb9d](https://github.com/vitalvas/nat-pmp/commit/429eb9dfd1cbd82c99a5d13ec35489e83b360c4f))

## [0.3.0](https://github.com/vitalvas/nat-pmp/compare/v0.2.0...v0.3.0) (2026-07-04)


### Features

* resolve internal address from an interface via internal_iface ([1b16fab](https://github.com/vitalvas/nat-pmp/commit/1b16fabfa11a4ea3d4186854477a28e9b5fbad9b))
* target a specific LAN address per mapping via internal_address ([c846f97](https://github.com/vitalvas/nat-pmp/commit/c846f97fae9885e2b7748cf5b8fe6b2fd9d5452d))

## [0.2.0](https://github.com/vitalvas/nat-pmp/compare/v0.1.0...v0.2.0) (2026-07-04)


### Features

* add --version flag reporting the build version ([bbfc3f4](https://github.com/vitalvas/nat-pmp/commit/bbfc3f47a5633b5ef63d37be6b72a3d1dee8ebd9))
* gate mappings on a local listener via require_listener ([0dbe933](https://github.com/vitalvas/nat-pmp/commit/0dbe933f7f1baf16ebc7c7e9a9adee19fa9c4a9d))

## [0.1.0](https://github.com/vitalvas/nat-pmp/compare/v0.0.1...v0.1.0) (2026-07-04)


### Features

* add systemd packaging and nfpm deb release tooling ([3a42760](https://github.com/vitalvas/nat-pmp/commit/3a427607df204f7e9ab70af5911d4ebd4611a175))
* init ([7b10504](https://github.com/vitalvas/nat-pmp/commit/7b10504149ec8ddb785ca8d0f7a817743ddf217e))
