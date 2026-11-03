# Third-party notices

## pingcli-plugin-terraformer

Some DaVinci node property shapes in
`internal/davinci/testdata/source/flow_*.json` (the `customErrorMessage`,
`AEqualsB`, `saveValue` and `startUiSubFlow` node `properties`) are copied
from the flow fixtures in `tests/testdata/flows/` of
https://github.com/pingidentity/pingcli-plugin-terraformer (commit
da68199761e9413692aa87bd1b39a6aa93337448).

The only identifier in those blobs, a subflow ID, was replaced with a fake ID.
The surrounding flow, connector, variable and application documents were
written for this repository and contain no tenant data.

Copyright Ping Identity Corporation. Licensed under the Apache License,
Version 2.0. You may obtain a copy of the License at
http://www.apache.org/licenses/LICENSE-2.0. Distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.

## pingone-go-client

The DaVinci API paths, collection keys and field names used by
`internal/davinci` were verified against
https://github.com/pingidentity/pingone-go-client v0.13.0 (Apache License 2.0).
No code was copied.
