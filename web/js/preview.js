// preview.js -- Main browser-side logic for vim-markdown-preview.
// Connects to the Go server via WebSocket, receives markdown content,
// renders it using markdown-it with plugins, and handles scroll sync.
(function () {
    'use strict';

    // Guard: markdown-it must be loaded before this script runs.
    if (typeof window.markdownit !== 'function') {
        document.body.appendChild(createBanner('Error: markdown-it failed to load. Preview will not work.'));
        return;
    }

    var md = null;
    var previousContent = '';
    var currentBufnr = -1;
    var ws = null;
    var reconnectTimer = null;
    var reconnectDelay = 1000;
    var maxReconnectAttempts = 20;
    var reconnectAttempts = 0;

    // Theme toggle support.
    // When the user manually toggles, this flag prevents incoming editor
    // theme data from overriding their choice.
    window._userThemeOverride = false;

    // Update the toggle button label to reflect what clicking it will do.
    function updateToggleLabel() {
        var btn = document.getElementById('theme-toggle');
        if (!btn) return;
        var current = document.documentElement.dataset.theme ||
            (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
        if (current === 'dark') {
            btn.textContent = '\u2600 Light';
            btn.setAttribute('aria-label', 'Switch to light theme');
        } else {
            btn.textContent = '\u263D Dark';
            btn.setAttribute('aria-label', 'Switch to dark theme');
        }
    }

    // Restore saved theme preference from localStorage.
    var savedTheme = localStorage.getItem('mkdp-theme');
    if (savedTheme) {
        document.documentElement.dataset.theme = savedTheme;
        window._userThemeOverride = true;
    }
    updateToggleLabel();

    // Bind the toggle button click handler.
    var toggleBtn = document.getElementById('theme-toggle');
    if (toggleBtn) {
        toggleBtn.addEventListener('click', function () {
            var current = document.documentElement.dataset.theme ||
                (window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light');
            var next = current === 'dark' ? 'light' : 'dark';
            document.documentElement.dataset.theme = next;
            localStorage.setItem('mkdp-theme', next);
            window._userThemeOverride = true;
            updateToggleLabel();
        });
    }

    // createBanner creates a fixed top banner with standard error styles.
    // text is the message to display; id is an optional element ID used by
    // callers that guard against duplicate insertion via getElementById.
    function createBanner(text, id) {
        var el = document.createElement('div');
        if (id) el.id = id;
        el.style.cssText = 'position:fixed;top:0;left:0;right:0;padding:12px;background:#dc3545;color:#fff;text-align:center;z-index:9999;font-family:sans-serif';
        el.textContent = text;
        return el;
    }

    // Show a fixed banner when the WebSocket connection is lost permanently.
    function showDisconnectedBanner() {
        var id = 'ws-disconnected-banner';
        if (document.getElementById(id)) return;
        document.body.appendChild(createBanner('Disconnected from preview server.', id));
    }

    // Initialize markdown-it with the plugin chain.
    function initMarkdownIt(options) {
        // Build the highlight function -- this must always be a function,
        // even if user options try to override it.
        var highlightFn = function (str, lang) {
            if (typeof hljs !== 'undefined' && lang && hljs.getLanguage(lang)) {
                try {
                    return '<pre class="hljs"><code>' +
                        hljs.highlight(str, { language: lang }).value +
                        '</code></pre>';
                } catch (_) { /* fall through */ }
            }
            return '';
        };

        // Apply user mkit options first, then re-enforce highlight so
        // user config cannot replace it with a non-function.
        var mkitOpts = Object.assign({
            html: true,
            xhtmlOut: true,
            linkify: true,
            typographer: true
        }, options.mkit || {}, { highlight: highlightFn });

        md = window.markdownit(mkitOpts);

        // Published plugins (loaded globally from vendor scripts).
        // Each entry is { global, opts? }. The helper skips missing globals
        // so load order failures degrade gracefully.
        function applyPlugins(list) {
            list.forEach(function (entry) {
                var plugin = window[entry.global];
                if (plugin) {
                    md.use.apply(md, entry.opts !== undefined ? [plugin, entry.opts] : [plugin]);
                }
            });
        }

        applyPlugins([
            { global: 'markdownitEmoji' },
            { global: 'markdownitDeflist' },
            { global: 'markdownitFootnote' },
            { global: 'markdownitTaskLists' }
        ]);

        // Shared slugify for heading IDs and TOC links. Both
        // markdown-it-anchor and markdown-it-toc-done-right must use the
        // same function or TOC hrefs won't match heading IDs.
        var slugify = function (s) {
            return s
                .toLowerCase()
                .trim()
                .replace(/[\s]+/g, '-')
                .replace(/[^\w-]/g, '');
        };

        if (typeof window.markdownItAnchor !== 'undefined') {
            md.use(window.markdownItAnchor, {
                permalink: window.markdownItAnchor.permalink
                    ? window.markdownItAnchor.permalink.headerLink()
                    : false,
                slugify: slugify
            });
        }

        // Force slugify so user-supplied toc options can't re-break link matching.
        var tocOpts = Object.assign({ listType: 'ul' }, options.toc || {}, { slugify: slugify });
        applyPlugins([
            { global: 'markdownItTocDoneRight', opts: tocOpts }
        ]);

        // Custom plugins (project-specific, loaded from js/plugins/).
        // metaPlugin has an extra condition so it stays inline.
        if (window.metaPlugin && options.hide_yaml_meta !== 0) md.use(window.metaPlugin);

        applyPlugins([
            { global: 'imagePlugin' },
            { global: 'imsizePlugin' },
            { global: 'linenumbersPlugin' },
            { global: 'katexPlugin', opts: options.katex || {} },
            { global: 'blockPlantumlPlugin', opts: options.uml || {} },
            { global: 'plantumlPlugin', opts: options.uml || {} },
            { global: 'mermaidPlugin' },
            { global: 'chartPlugin' },
            { global: 'diagramPlugin' },
            { global: 'flowchartPlugin' },
            { global: 'dotPlugin' }
        ]);
    }

    // Connect to the WebSocket server for the given buffer number.
    function connect(bufnr) {
        if (reconnectTimer) {
            clearTimeout(reconnectTimer);
            reconnectTimer = null;
        }

        currentBufnr = bufnr;

        if (ws) {
            ws.close();
            ws = null;
        }

        var protocol = location.protocol === 'https:' ? 'wss:' : 'ws:';
        var url = protocol + '//' + location.host + '/ws?bufnr=' + bufnr;

        ws = new WebSocket(url);

        ws.onopen = function () {
            reconnectDelay = 1000;
            reconnectAttempts = 0;
        };

        ws.onmessage = function (event) {
            var msg;
            try {
                msg = JSON.parse(event.data);
            } catch (e) {
                console.error('failed to parse ws message:', e);
                return;
            }

            switch (msg.event) {
                case 'refresh_content':
                    onRefreshContent(msg.data);
                    break;
                case 'close_page':
                    window.close();
                    // window.close() only works on script-opened windows.
                    // Close the WebSocket with application-level code 4001
                    // so the onclose handler does not schedule a reconnect.
                    // RFC 6455 reserves 1001 (Going Away) for the transport
                    // layer; application code may only use 1000 or 3000-4999.
                    // Codes 1000 and 4001 suppress reconnection. This prevents
                    // user-opened tabs from reconnecting after a close_page
                    // and continuously re-displaying stale content.
                    if (ws) {
                        ws.close(4001, 'close_page');
                    }
                    showDisconnectedBanner();
                    break;
                default:
                    console.warn('unrecognized WebSocket event:', msg.event);
                    break;
            }
        };

        ws.onclose = function (evt) {
            // Clear the stale reference so code between onclose and the
            // reconnect timer firing sees null rather than a CLOSED socket.
            ws = null;

            // Don't reconnect on clean closure. 1000 = normal close from
            // the server; 1001 = Going Away sent by the server on shutdown;
            // 4001 = application-level close sent by the close_page handler.
            if (evt.code === 1000 || evt.code === 1001 || evt.code === 4001) {
                return;
            }

            reconnectAttempts++;
            if (reconnectAttempts > maxReconnectAttempts) {
                showDisconnectedBanner();
                return;
            }

            // Auto-reconnect with exponential backoff.
            // Schedule the timeout with the current delay first, then double
            // for the next attempt. This produces the sequence 1s, 2s, 4s,
            // 8s, 16s, 30s (capped). Doubling before scheduling would cause
            // the first reconnect to fire at 2s instead of the intended 1s.
            reconnectTimer = setTimeout(function () {
                connect(currentBufnr);
            }, reconnectDelay);
            reconnectDelay = Math.min(reconnectDelay * 2, 30000);
        };

        ws.onerror = function () {
            // onclose will fire after onerror; reconnection handled there.
        };
    }

    // Handle incoming refresh_content messages.
    function onRefreshContent(data) {
        if (!data) return;

        var opts = data.options || {};

        // Initialize markdown-it on first refresh. The instance is created
        // once and reused for the lifetime of the page. markdown-it's core
        // options (html, linkify, typographer, etc.) are frozen at
        // construction time and cannot be reconfigured on an existing
        // instance, so any g:mkdp_preview_options.mkit changes only take
        // effect after a page reload.
        if (!md) {
            initMarkdownIt(opts);
        }

        // Apply theme from editor, unless the user manually toggled.
        if (data.theme && !window._userThemeOverride) {
            document.documentElement.dataset.theme = data.theme;
            updateToggleLabel();
        }

        // Update page title and filename header.
        var basename = data.name
            ? data.name.split(/[/\\]/).pop().replace(/\.[^.]+$/, '')
            : '';

        if (data.pageTitle) {
            var title = data.pageTitle;
            if (basename) {
                title = title.replace('${name}', basename);
            }
            document.title = title;
        }

        var filenameEl = document.getElementById('filename');
        if (filenameEl && basename) {
            filenameEl.textContent = basename;
        }

        // Render content if changed.
        var renderEl = document.getElementById('content');
        var content = Array.isArray(data.content) ? data.content.join('\n') : (data.content || '');
        if (content !== previousContent) {
            previousContent = content;
            if (renderEl && md) {
                try {
                    renderEl.innerHTML = md.render(content);
                } catch (renderErr) {
                    // Show raw content as fallback so the page is never
                    // stuck on the loading skeleton.
                    console.error('markdown-it render failed:', renderErr);
                    var escaped = content
                        .replace(/&/g, '&amp;')
                        .replace(/</g, '&lt;')
                        .replace(/>/g, '&gt;');
                    renderEl.innerHTML = '<pre style="white-space:pre-wrap">' + escaped + '</pre>';
                }

                // Post-render: invoke diagram libraries on their placeholders.
                // Safe to call even after a render failure (they scan for their
                // own placeholder elements and find nothing).
                if (window.renderMermaid) window.renderMermaid(opts);
                if (window.renderCharts) window.renderCharts();
                if (window.renderSequenceDiagrams) window.renderSequenceDiagrams(opts.sequence_diagrams || {});
                if (window.renderFlowcharts) window.renderFlowcharts(opts.flowchart_diagrams || {});
                if (window.renderDot) window.renderDot();
            }
        }

        // Scroll sync: runs on every refresh (not just content changes)
        // so cursor-only movements still scroll the preview.
        if (window.scrollToLine && !opts.disable_sync_scroll) {
            window.scrollToLine(
                data.cursor,
                data.winline,
                data.winheight,
                Array.isArray(data.content) ? data.content.length : 0,
                opts.sync_scroll_type || 'middle'
            );
        }

        // Content-editable support.
        if (renderEl && opts.content_editable) {
            renderEl.contentEditable = 'true';
        }
    }

    // Parse buffer number from URL path: /page/{bufnr}.
    var pathParts = location.pathname.split('/');
    var bufnr = parseInt(pathParts[pathParts.length - 1], 10) || 1;

    // If the server embedded cached content in the HTML response,
    // decode and render it synchronously before connecting the
    // WebSocket. This eliminates the flash between page load and
    // WebSocket delivery. The content !== previousContent guard in
    // onRefreshContent prevents double-rendering when the WebSocket
    // later replays the same cached message.
    var initialEl = document.getElementById('initial-data');
    if (initialEl && initialEl.dataset.payload) {
        try {
            var msg = JSON.parse(atob(initialEl.dataset.payload));
            if (msg.event === 'refresh_content' && msg.data) {
                onRefreshContent(msg.data);
            }
        } catch (e) {
            console.warn('failed to parse initial data:', e);
        }
        initialEl.remove();
    }

    // Start connection.
    connect(bufnr);
})();
