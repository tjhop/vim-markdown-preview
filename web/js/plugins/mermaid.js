// mermaid.js -- Mermaid diagram plugin for markdown-it.
// Detects ```mermaid fenced code blocks and auto-detected mermaid patterns
// (gantt, sequenceDiagram, graph, etc.). Generates <div class="mermaid">
// placeholders that are rendered post-DOM-update by renderMermaid().
(function () {
    'use strict';

    // Patterns that indicate mermaid content even without an explicit info string.
    var mermaidPatterns = /^(gantt|sequenceDiagram|classDiagram|stateDiagram|erDiagram|journey|gitGraph|graph\s|graph\n|flowchart\s|flowchart\n|pie\s|pie\n|mindmap|timeline|C4Context|C4Container|C4Component|C4Deployment|xychart|block-beta|sankey-beta|packet-beta|kanban|architecture|quadrantChart|requirementDiagram)/m;

    // Track the current mermaid theme to avoid redundant mermaid.initialize()
    // calls, which reset internal mermaid state and re-parse configuration.
    var currentTheme = null;

    // Cache of rendered mermaid SVG keyed by raw source text. When innerHTML
    // is reassigned on content refresh, data-rendered attributes are lost and
    // all diagrams appear unrendered. This cache restores previously rendered
    // SVG without calling mermaid.run(), avoiding flicker and ID collisions.
    // Bounded to maxCacheSize entries to prevent unbounded memory growth
    // in long editing sessions with many diagram iterations.
    var mermaidCache = {};
    var maxCacheSize = 64;

    function mermaidPlugin(md) {
        var defaultFence = md.renderer.rules.fence ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.fence = function (tokens, idx, options, env, self) {
            var token = tokens[idx];
            var info = token.info ? token.info.trim().toLowerCase() : '';
            var content = token.content;

            var isMermaid = (info === 'mermaid') ||
                (!info && mermaidPatterns.test(content));

            if (!isMermaid || !content.trim()) {
                return defaultFence(tokens, idx, options, env, self);
            }

            // Escape HTML entities in the content for safe embedding.
            var escaped = md.utils.escapeHtml(content);
            return '<div class="mermaid">' + escaped + '</div>\n';
        };
    }

    function renderMermaid(options) {
        if (typeof mermaid === 'undefined') return;

        var elements = document.querySelectorAll('.mermaid:not([data-rendered])');
        if (elements.length === 0) return;

        var theme = (options && options.mermaid && options.mermaid.theme) || 'default';
        if (theme !== currentTheme) {
            mermaid.initialize({
                startOnLoad: false,
                theme: theme,
                securityLevel: 'strict'
            });
            currentTheme = theme;
            // Theme changed -- cached SVGs use the old theme's styling.
            mermaidCache = {};
        }

        // Split elements into cached (restore directly) and uncached (need
        // mermaid.run). Capture source text before mermaid replaces it.
        var uncached = [];
        var uncachedSources = [];
        elements.forEach(function (el) {
            var source = el.textContent.trim();
            if (mermaidCache[source]) {
                el.innerHTML = mermaidCache[source];
                el.dataset.rendered = 'true';
            } else {
                uncachedSources.push(source);
                uncached.push(el);
            }
        });

        if (uncached.length === 0) return;

        mermaid.run({ nodes: uncached }).then(function () {
            uncached.forEach(function (el, i) {
                el.dataset.rendered = 'true';
                mermaidCache[uncachedSources[i]] = el.innerHTML;
            });

            // Evict oldest entries when cache exceeds maxCacheSize.
            var keys = Object.keys(mermaidCache);
            if (keys.length > maxCacheSize) {
                keys.slice(0, keys.length - maxCacheSize).forEach(function (k) {
                    delete mermaidCache[k];
                });
            }
        }).catch(function (err) {
            console.error('mermaid render error:', err);
            // Mark failed elements so they are not retried on every
            // content refresh. Without this, permanently broken diagrams
            // (e.g., syntax errors) cause an infinite render-error loop.
            uncached.forEach(function (el) {
                el.dataset.rendered = 'error';
            });
        });
    }

    window.mermaidPlugin = mermaidPlugin;
    window.renderMermaid = renderMermaid;
})();
