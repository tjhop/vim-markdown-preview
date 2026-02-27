// diagram.js -- Sequence diagram plugin for markdown-it.
// Detects ```sequence, ```sequence-diagrams, or ```seq fenced code blocks.
// Generates <div class="sequence-diagram"> placeholders that are rendered
// post-DOM-update by renderSequenceDiagrams().
(function () {
    'use strict';

    function diagramPlugin(md) {
        var defaultFence = md.renderer.rules.fence ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.fence = function (tokens, idx, options, env, self) {
            var token = tokens[idx];
            var info = token.info ? token.info.trim().toLowerCase() : '';

            if (info !== 'sequence' && info !== 'sequence-diagrams' && info !== 'seq') {
                return defaultFence(tokens, idx, options, env, self);
            }

            var escaped = md.utils.escapeHtml(token.content);
            return '<div class="sequence-diagram">' + escaped + '</div>\n';
        };
    }

    function renderSequenceDiagrams(options) {
        if (typeof Diagram === 'undefined') return;

        var elements = document.querySelectorAll('.sequence-diagram:not([data-rendered])');
        elements.forEach(function (el) {
            var content = el.textContent;
            el.textContent = '';

            try {
                var diagram = Diagram.parse(content);
                var theme = (options && options.theme) || 'simple';
                diagram.drawSVG(el, { theme: theme });
                el.dataset.rendered = 'true';
            } catch (e) {
                console.error('Sequence diagram render error:', e);
                el.dataset.rendered = 'error';
                el.textContent = content;
            }
        });
    }

    window.diagramPlugin = diagramPlugin;
    window.renderSequenceDiagrams = renderSequenceDiagrams;
})();
