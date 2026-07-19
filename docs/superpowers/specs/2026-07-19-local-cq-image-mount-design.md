# Local CQ Image Mount Design

## Goal

Allow keyword answers to reference images stored under the deployment's `data/media/` directory without adding an image hosting service, while continuing to support HTTP and HTTPS image URLs.

## Design

WPS answers use a fixed relative-path form such as `[CQ:image,file=maps/campus.png]`. The parser maps the relative path to `file:///app/jxh-media/maps/campus.png`; Docker Compose mounts `./data/media` read-only at `/app/jxh-media` in the NapCat container. NapCat remains responsible for reading and sending the image.

Remote `url` values accept both HTTP and HTTPS and take precedence over `file`. A `file` value may also be an HTTP or HTTPS URL. Local values must be non-empty relative paths using forward slashes. Absolute paths, backslashes, `.`/`..` traversal, `file://`, `base64://`, query strings, and fragments are rejected. Unicode, spaces, and nested directories are URI-encoded when generating the OneBot image source.

Invalid or unreadable images retain the existing text fallback behavior. Image CQ data remains excluded from AI retrieval and prompts. No new service, configuration option, or database field is introduced.

## Verification

Parser tests cover URL precedence, HTTP support, local URI conversion, Unicode and spaces, path traversal, absolute paths, unsupported schemes, and ordered mixed replies. Compose validation confirms the read-only media mount. Full Go tests, race tests, vet, and the bot container build must pass.
