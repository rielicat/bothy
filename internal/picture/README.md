# internal/picture

Bothy's picture in the chat apps (`core.AgentPicture`). The default,
`default.png` and `default.jpg` (1024 px, the brand mark from
`web/public/icon-maskable.svg` centred so its ink reaches 61% of the way to
the edge of a circle crop), is embedded; `TestDefaultPicture` renders the mark
again, fails when the files drift from it and rewrites them with `-update`.
An admin's picture is `<data>/picture.png`, written atomically through
`os.Root` (temp file, fsync, rename, 0600). `Set` sniffs the upload (PNG, JPEG
or WebP, at most 5 MB, whatever type the client claimed), reads the declared
dimensions before decoding and refuses more than 8000 px a side, fewer than
128, or a decode that would allocate over 160 MB; then it crops the centre
square, turns a JPEG upright by its EXIF orientation, flattens it onto white,
scales it to 1024 px (Catmull-Rom) and re-encodes it, which drops every bit
of metadata. `Current` reads the file back (one by hand goes through the same
checks), caches it by content and adds the JPEG Telegram needs. Imports: the
stdlib, `core` and `golang.org/x/image` (`draw`, `webp`).
