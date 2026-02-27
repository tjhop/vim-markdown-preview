// chart.js -- Chart.js diagram plugin for markdown-it.
// Detects ```chart fenced code blocks containing JSON chart config.
// Generates <canvas class="chartjs"> placeholders that are rendered
// post-DOM-update by renderCharts().
(function () {
    'use strict';

    function chartPlugin(md) {
        var defaultFence = md.renderer.rules.fence ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.fence = function (tokens, idx, options, env, self) {
            var token = tokens[idx];
            var info = token.info ? token.info.trim().toLowerCase() : '';

            if (info !== 'chart') {
                return defaultFence(tokens, idx, options, env, self);
            }

            // Store the raw JSON config as a data attribute.
            var escaped = md.utils.escapeHtml(token.content.trim());
            return '<canvas class="chartjs" data-chart="' + escaped + '"></canvas>\n';
        };
    }

    function renderCharts() {
        if (typeof Chart === 'undefined') return;

        var canvases = document.querySelectorAll('canvas.chartjs:not([data-rendered])');
        canvases.forEach(function (canvas) {
            var raw = canvas.getAttribute('data-chart');
            if (!raw) return;

            try {
                var config = JSON.parse(raw);
                new Chart(canvas, config);
                canvas.dataset.rendered = 'true';
            } catch (e) {
                console.error('Chart.js render error:', e);
                canvas.dataset.rendered = 'error';
                var errDiv = document.createElement('div');
                errDiv.className = 'chart-error';
                errDiv.textContent = 'Chart error: ' + e.message;
                canvas.parentNode.insertBefore(errDiv, canvas.nextSibling);
            }
        });
    }

    window.chartPlugin = chartPlugin;
    window.renderCharts = renderCharts;
})();
