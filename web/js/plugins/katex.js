// katex.js -- Math rendering plugin for markdown-it using KaTeX.
// Parses $...$ for inline math and $$...$$ for block (display) math.
// Renders via katex.renderToString() with error fallback.
(function () {
    'use strict';

    // Check whether a delimiter at the given position is valid:
    // not preceded/followed by whitespace for inline delimiters.
    function isValidDelim(state, pos) {
        var prevChar = pos > 0 ? state.src.charCodeAt(pos - 1) : -1;
        var nextChar = pos + 1 < state.posMax ? state.src.charCodeAt(pos + 1) : -1;

        var canOpen = true;
        var canClose = true;

        // Closing delimiter must not be preceded by whitespace.
        if (prevChar === 0x20 || prevChar === 0x09) {
            canClose = false;
        }
        // Opening delimiter must not be followed by whitespace.
        if (nextChar === 0x20 || nextChar === 0x09) {
            canOpen = false;
        }

        return { canOpen: canOpen, canClose: canClose };
    }

    function inlineMath(state, silent) {
        if (state.src.charCodeAt(state.pos) !== 0x24 /* $ */) return false;

        // Don't match $$ -- that's handled by block math.
        if (state.pos + 1 < state.posMax && state.src.charCodeAt(state.pos + 1) === 0x24) {
            return false;
        }

        var res = isValidDelim(state, state.pos);
        if (!res.canOpen) {
            if (!silent) state.pending += '$';
            state.pos++;
            return true;
        }

        var start = state.pos + 1;
        var match = start;

        // Find closing $.
        while ((match = state.src.indexOf('$', match)) !== -1) {
            // Check for escaping.
            var escaped = false;
            var p = match - 1;
            while (p >= start && state.src.charCodeAt(p) === 0x5C /* \ */) {
                escaped = !escaped;
                p--;
            }
            if (!escaped) break;
            match++;
        }

        if (match === -1 || match === start) {
            if (!silent) state.pending += '$';
            state.pos = start;
            return true;
        }

        var content = state.src.slice(start, match);
        // Don't allow newlines in inline math.
        if (content.indexOf('\n') !== -1) {
            if (!silent) state.pending += '$';
            state.pos = start;
            return true;
        }

        if (!silent) {
            var token = state.push('math_inline', 'math', 0);
            token.markup = '$';
            token.content = content.trim();
        }

        state.pos = match + 1;
        return true;
    }

    function blockMath(state, startLine, endLine, silent) {
        var pos = state.bMarks[startLine] + state.tShift[startLine];
        var max = state.eMarks[startLine];

        if (pos + 1 >= max) return false;
        if (state.src.charCodeAt(pos) !== 0x24 || state.src.charCodeAt(pos + 1) !== 0x24) {
            return false;
        }

        // Found opening $$.
        pos += 2;
        var firstLine = state.src.slice(pos, max).trim();

        var found = false;
        var nextLine = startLine;
        var content = '';

        // Check if $$ ... $$ on same line.
        if (firstLine.length > 0 && firstLine.slice(-2) === '$$') {
            content = firstLine.slice(0, -2).trim();
            found = true;
        }

        if (!found) {
            nextLine = startLine + 1;
            while (nextLine < endLine) {
                var linePos = state.bMarks[nextLine] + state.tShift[nextLine];
                var lineMax = state.eMarks[nextLine];
                var line = state.src.slice(linePos, lineMax).trim();

                if (line === '$$') {
                    found = true;
                    break;
                }
                nextLine++;
            }

            if (!found) return false;
        }

        // In silent mode, just confirm the rule matches (closing $$ exists)
        // without producing tokens.
        if (silent) return true;

        // Gather content lines for the multi-line case (same-line case
        // already populated content above).
        if (!content) {
            var lines = [];
            if (firstLine.length > 0) lines.push(firstLine);
            for (var i = startLine + 1; i < nextLine; i++) {
                lines.push(state.src.slice(
                    state.bMarks[i] + state.tShift[i],
                    state.eMarks[i]
                ));
            }
            content = lines.join('\n');
        }

        var token = state.push('math_block', 'math', 0);
        token.block = true;
        token.content = content;
        token.markup = '$$';
        token.map = [startLine, nextLine + 1];
        state.line = nextLine + 1;
        return true;
    }

    function katexPlugin(md, options) {
        var katexOptions = Object.assign({
            throwOnError: false,
            errorColor: '#cc0000'
        }, options || {});

        md.inline.ruler.after('escape', 'math_inline', inlineMath);
        md.block.ruler.after('blockquote', 'math_block', blockMath, {
            alt: ['paragraph', 'reference', 'blockquote', 'list']
        });

        md.renderer.rules.math_inline = function (tokens, idx) {
            var content = tokens[idx].content;
            try {
                return katex.renderToString(content, Object.assign({}, katexOptions, { displayMode: false }));
            } catch (e) {
                return '<span class="katex-error" title="' +
                    md.utils.escapeHtml(e.toString()) + '">' +
                    md.utils.escapeHtml(content) + '</span>';
            }
        };

        md.renderer.rules.math_block = function (tokens, idx) {
            var content = tokens[idx].content;
            try {
                return '<p class="katex-block">' +
                    katex.renderToString(content, Object.assign({}, katexOptions, { displayMode: true })) +
                    '</p>\n';
            } catch (e) {
                return '<p class="katex-block katex-error" title="' +
                    md.utils.escapeHtml(e.toString()) + '">' +
                    md.utils.escapeHtml(content) + '</p>\n';
            }
        };
    }

    window.katexPlugin = katexPlugin;
})();
