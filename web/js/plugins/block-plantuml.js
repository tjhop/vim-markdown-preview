// block-plantuml.js -- PlantUML block syntax plugin for markdown-it.
// Detects @startuml...@enduml blocks and renders them as images via
// a PlantUML server (default: https://www.plantuml.com/plantuml).
(function () {
    'use strict';

    // Shared PlantUML defaults and URL builder, consumed by plantuml.js
    // (which loads after this file). Centralised here to avoid duplicating
    // the server URL and image format defaults across both plugins.
    var plantumlDefaults = { server: 'https://www.plantuml.com/plantuml', imageFormat: 'svg' };

    function plantumlImageURL(server, format, encoded) {
        return server + '/' + format + '/' + encoded;
    }

    window._plantumlShared = { defaults: plantumlDefaults, imageURL: plantumlImageURL };

    function blockPlantumlPlugin(md, options) {
        var opts = Object.assign({}, plantumlDefaults, options || {});

        md.block.ruler.before('fence', 'plantuml_block', function plantumlBlock(state, startLine, endLine, silent) {
            var pos = state.bMarks[startLine] + state.tShift[startLine];
            var max = state.eMarks[startLine];
            var line = state.src.slice(pos, max).trim();

            if (!/^@startuml/.test(line)) return false;

            // Scan for @enduml.
            var nextLine = startLine + 1;
            var found = false;
            while (nextLine < endLine) {
                var lineStart = state.bMarks[nextLine] + state.tShift[nextLine];
                var lineEnd = state.eMarks[nextLine];
                var curLine = state.src.slice(lineStart, lineEnd).trim();
                if (/^@enduml/.test(curLine)) {
                    found = true;
                    break;
                }
                nextLine++;
            }

            if (!found) return false;
            if (silent) return true;

            // Gather content between markers (inclusive of @startuml/@enduml
            // for proper PlantUML encoding).
            var lines = [];
            for (var i = startLine; i <= nextLine; i++) {
                lines.push(state.src.slice(
                    state.bMarks[i] + state.tShift[i],
                    state.eMarks[i]
                ));
            }
            var content = lines.join('\n');

            // Encode and build URL.
            var encoded = '';
            if (typeof plantumlEncoder !== 'undefined' && plantumlEncoder.encode) {
                encoded = plantumlEncoder.encode(content);
            }

            var token = state.push('plantuml_block', '', 0);
            token.content = content;
            token.markup = '@startuml/@enduml';
            token.map = [startLine, nextLine + 1];
            token.meta = { encoded: encoded, server: opts.server, format: opts.imageFormat };
            state.line = nextLine + 1;
            return true;
        });

        md.renderer.rules.plantuml_block = function (tokens, idx) {
            var meta = tokens[idx].meta;
            if (!meta.encoded) {
                return '<pre>' + md.utils.escapeHtml(tokens[idx].content) + '</pre>\n';
            }
            var url = plantumlImageURL(meta.server, meta.format, meta.encoded);
            return '<p><img src="' + md.utils.escapeHtml(url) + '" alt="PlantUML diagram"></p>\n';
        };
    }

    window.blockPlantumlPlugin = blockPlantumlPlugin;
})();
