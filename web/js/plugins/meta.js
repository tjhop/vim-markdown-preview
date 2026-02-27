// meta.js -- YAML front-matter hiding plugin for markdown-it.
// Detects --- delimited YAML blocks at the start of the document and
// swallows them so they don't appear in rendered output.
(function () {
    'use strict';

    function metaPlugin(md) {
        md.block.ruler.before('code', 'meta', function meta(state, startLine, endLine, silent) {
            // Must start at the very beginning of the document.
            if (startLine !== 0) return false;

            // First line must be exactly '---'.
            var startPos = state.bMarks[startLine] + state.tShift[startLine];
            var maxPos = state.eMarks[startLine];
            if (state.src.slice(startPos, maxPos).trim() !== '---') return false;

            // Scan forward for the closing '---'.
            var nextLine = startLine + 1;
            while (nextLine < endLine) {
                var lineStart = state.bMarks[nextLine] + state.tShift[nextLine];
                var lineEnd = state.eMarks[nextLine];
                if (state.src.slice(lineStart, lineEnd).trim() === '---') {
                    // Found closing delimiter.
                    if (silent) return true;
                    state.line = nextLine + 1;
                    return true;
                }
                nextLine++;
            }

            return false;
        });
    }

    window.metaPlugin = metaPlugin;
})();
