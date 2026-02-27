// dot.js -- Graphviz/dot plugin for markdown-it.
// Detects ```dot or ```graphviz fenced code blocks.
// Generates <div class="dot"> placeholders that are rendered
// post-DOM-update by renderDot() using Viz.js.
(function () {
    'use strict';

    function dotPlugin(md) {
        var defaultFence = md.renderer.rules.fence ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.fence = function (tokens, idx, options, env, self) {
            var token = tokens[idx];
            var info = token.info ? token.info.trim().toLowerCase() : '';

            if (info !== 'dot' && info !== 'graphviz') {
                return defaultFence(tokens, idx, options, env, self);
            }

            var escaped = md.utils.escapeHtml(token.content);
            return '<div class="dot">' + escaped + '</div>\n';
        };
    }

    // Lazy singleton for the Viz.js v2.x instance. Created once on first
    // renderDot call and reused across subsequent renders to avoid the
    // overhead of repeated construction.
    var vizInstance = null;
    var vizInitialized = false;

    function getVizInstance() {
        if (vizInitialized) return vizInstance;
        vizInitialized = true;
        try {
            vizInstance = new Viz({ Module: typeof Module !== 'undefined' ? Module : undefined,
                                    render: typeof render !== 'undefined' ? render : undefined });
        } catch (_) {
            // Viz.js v1.x fallback: Viz is a function directly.
            vizInstance = null;
        }
        return vizInstance;
    }

    function renderDot() {
        if (typeof Viz === 'undefined') return;

        var elements = document.querySelectorAll('.dot:not([data-rendered])');
        if (elements.length === 0) return;

        var viz = getVizInstance();

        elements.forEach(function (el) {
            var content = el.textContent;
            el.textContent = '';

            try {
                if (viz && typeof viz.renderSVGElement === 'function') {
                    // v2.x async API.
                    viz.renderSVGElement(content).then(function (svgEl) {
                        el.appendChild(svgEl);
                        el.dataset.rendered = 'true';
                    }).catch(function (e) {
                        console.error('Viz.js render error:', e);
                        el.textContent = content;
                        // A render error corrupts the Viz.js v2 instance;
                        // reset the singleton so the next call recreates it.
                        vizInstance = null;
                        vizInitialized = false;
                    });
                } else if (typeof Viz === 'function') {
                    // v1.x sync API.
                    var svgStr = Viz(content);
                    el.innerHTML = svgStr;
                    el.dataset.rendered = 'true';
                } else {
                    el.textContent = content;
                }
            } catch (e) {
                console.error('Viz.js render error:', e);
                el.textContent = content;
            }
        });
    }

    window.dotPlugin = dotPlugin;
    window.renderDot = renderDot;
})();
