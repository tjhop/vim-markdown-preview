// flowchart.js -- Flowchart plugin for markdown-it.
// Detects ```flowchart or ```flow fenced code blocks.
// Generates <div class="flowchart"> placeholders that are rendered
// post-DOM-update by renderFlowcharts().
(function () {
    'use strict';

    function flowchartPlugin(md) {
        var defaultFence = md.renderer.rules.fence ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.fence = function (tokens, idx, options, env, self) {
            var token = tokens[idx];
            var info = token.info ? token.info.trim().toLowerCase() : '';

            if (info !== 'flowchart' && info !== 'flow') {
                return defaultFence(tokens, idx, options, env, self);
            }

            var escaped = md.utils.escapeHtml(token.content);
            return '<div class="flowchart">' + escaped + '</div>\n';
        };
    }

    function renderFlowcharts(options) {
        if (typeof flowchart === 'undefined') return;

        var elements = document.querySelectorAll('.flowchart:not([data-rendered])');
        elements.forEach(function (el) {
            var content = el.textContent;
            el.textContent = '';

            try {
                var diagram = flowchart.parse(content);
                diagram.drawSVG(el, options || {});
                el.dataset.rendered = 'true';
            } catch (e) {
                console.error('Flowchart render error:', e);
                el.dataset.rendered = 'error';
                el.textContent = content;
            }
        });
    }

    window.flowchartPlugin = flowchartPlugin;
    window.renderFlowcharts = renderFlowcharts;
})();
