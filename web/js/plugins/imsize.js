// imsize.js -- Image size syntax plugin for markdown-it.
// Extends the standard image syntax with optional size:
//   ![alt](url =WIDTHxHEIGHT)
// Width/height can be numbers, percentages, or empty (auto).
// Adapted from markdown-it-imsize.
(function () {
    'use strict';

    // Parse the =WIDTHxHEIGHT suffix from inside the image URL parentheses.
    // Returns { width, height, pos } or null.
    function parseImageSize(str, pos, max) {
        if (pos >= max) return null;
        if (str.charCodeAt(pos) !== 0x3D /* = */) return null;
        pos++;

        // Parse width (digits, %, or empty).
        var width = '';
        while (pos < max) {
            var code = str.charCodeAt(pos);
            if (code === 0x78 /* x */) break;
            if (code === 0x20 /* space */ || code === 0x29 /* ) */) return null;
            width += str.charAt(pos);
            pos++;
        }

        if (pos >= max || str.charCodeAt(pos) !== 0x78 /* x */) return null;
        pos++;

        // Parse height (digits, %, or empty).
        var height = '';
        while (pos < max) {
            var ch = str.charCodeAt(pos);
            if (ch === 0x20 /* space */ || ch === 0x29 /* ) */) break;
            height += str.charAt(pos);
            pos++;
        }

        return { width: width, height: height, pos: pos };
    }

    // Parse a link destination that may end with =WxH size.
    // Returns { href, width, height, pos } or null.
    function parseLink(str, pos, max) {
        var href = '';

        if (pos < max && str.charCodeAt(pos) === 0x3C /* < */) {
            // <url> syntax -- not commonly used with images, pass through.
            return null;
        }

        var level = 0;
        var start = pos;
        while (pos < max) {
            var code = str.charCodeAt(pos);

            if (code === 0x20 /* space */) break;

            // Detect =WxH before the closing paren.
            if (code === 0x3D /* = */ && pos > start) {
                var sizeResult = parseImageSize(str, pos, max);
                if (sizeResult) {
                    href = str.slice(start, pos);
                    return {
                        href: href,
                        width: sizeResult.width,
                        height: sizeResult.height,
                        pos: sizeResult.pos
                    };
                }
            }

            if (code === 0x28 /* ( */) {
                level++;
                if (level > 1) return null;
            }
            if (code === 0x29 /* ) */) {
                if (level === 0) break;
                level--;
            }

            // Backslash escape.
            if (code === 0x5C /* \ */ && pos + 1 < max) {
                pos += 2;
                continue;
            }

            pos++;
        }

        if (start === pos) return null;

        href = str.slice(start, pos);
        return { href: href, width: '', height: '', pos: pos };
    }

    // Parse the image title inside quotes after the URL.
    function parseTitle(str, pos, max) {
        if (pos >= max) return { title: '', pos: pos };

        var marker = str.charCodeAt(pos);
        if (marker !== 0x22 /* " */ && marker !== 0x27 /* ' */ && marker !== 0x28 /* ( */) {
            return { title: '', pos: pos };
        }

        var closeMarker = marker === 0x28 ? 0x29 : marker;
        pos++;
        var title = '';
        while (pos < max) {
            var code = str.charCodeAt(pos);
            if (code === closeMarker) {
                return { title: title, pos: pos + 1 };
            }
            if (code === 0x5C /* \ */ && pos + 1 < max) {
                pos++;
                title += str.charAt(pos);
            } else {
                title += str.charAt(pos);
            }
            pos++;
        }
        return null;
    }

    // Skip spaces (0x20) and newlines (0x0A) starting at pos.
    function skipWhitespace(str, pos, max) {
        while (pos < max && (str.charCodeAt(pos) === 0x20 || str.charCodeAt(pos) === 0x0A)) {
            pos++;
        }
        return pos;
    }

    function imsizePlugin(md) {
        md.inline.ruler.before('image', 'imsize', function imageWithSize(state, silent) {
            var pos = state.pos;
            var max = state.posMax;

            // Must start with ![
            if (state.src.charCodeAt(pos) !== 0x21 /* ! */) return false;
            if (pos + 1 >= max || state.src.charCodeAt(pos + 1) !== 0x5B /* [ */) return false;

            var labelStart = pos + 2;
            var labelEnd = state.md.helpers.parseLinkLabel(state, pos + 1, false);
            if (labelEnd < 0) return false;

            pos = labelEnd + 1;
            if (pos >= max || state.src.charCodeAt(pos) !== 0x28 /* ( */) return false;
            pos++;

            pos = skipWhitespace(state.src, pos, max);

            // Parse the href (possibly with =WxH).
            var linkResult = parseLink(state.src, pos, max);
            if (!linkResult) return false;
            var href = linkResult.href;
            var width = linkResult.width;
            var height = linkResult.height;
            pos = linkResult.pos;

            pos = skipWhitespace(state.src, pos, max);

            // Optionally parse title.
            var title = '';
            if (pos < max && state.src.charCodeAt(pos) !== 0x29 /* ) */) {
                var titleResult = parseTitle(state.src, pos, max);
                if (!titleResult) return false;
                title = titleResult.title;
                pos = titleResult.pos;
            }

            // If =WxH wasn't found in the URL part, check after the title.
            if (!width && !height) {
                pos = skipWhitespace(state.src, pos, max);
                if (pos < max && state.src.charCodeAt(pos) === 0x3D /* = */) {
                    var afterTitleSize = parseImageSize(state.src, pos, max);
                    if (afterTitleSize) {
                        width = afterTitleSize.width;
                        height = afterTitleSize.height;
                        pos = afterTitleSize.pos;
                    }
                }
            }

            pos = skipWhitespace(state.src, pos, max);

            if (pos >= max || state.src.charCodeAt(pos) !== 0x29 /* ) */) return false;
            pos++;

            // Advance past the matched image syntax. This must happen
            // before the silent check because markdown-it's skipToken
            // requires state.pos to advance when a rule returns true,
            // even in silent mode (probe-only). Without this, parsing
            // link-wrapped images like [![badge](img)](url) throws
            // "inline rule didn't increment state.pos".
            state.pos = pos;

            if (silent) return true;

            // Build the token.
            // Follow the same pattern as markdown-it's built-in image rule:
            // parse alt text into child tokens via inline.parse().
            var content = state.src.slice(labelStart, labelEnd);
            var children = [];
            state.md.inline.parse(content, state.md, state.env, children);

            var token = state.push('image', 'img', 0);
            token.attrs = [['src', href], ['alt', '']];
            token.children = children;
            token.content = content;
            token.attrSet('alt', state.md.utils.unescapeMd(content));
            if (title) {
                token.attrPush(['title', title]);
            }
            if (width) {
                token.attrPush(['width', width]);
            }
            if (height) {
                token.attrPush(['height', height]);
            }

            state.posMax = max;
            return true;
        });
    }

    window.imsizePlugin = imsizePlugin;
})();
