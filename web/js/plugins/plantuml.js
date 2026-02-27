// plantuml.js -- PlantUML fenced code plugin for markdown-it.
// Detects ```plantuml fenced code blocks and renders them as images
// via a PlantUML server.
(function () {
    'use strict';

    // Shared defaults and URL builder are defined in block-plantuml.js
    // (loaded before this file) and exposed via window._plantumlShared.
    var shared = window._plantumlShared || {};
    var plantumlDefaults = shared.defaults || { server: 'https://www.plantuml.com/plantuml', imageFormat: 'svg' };
    var plantumlImageURL = shared.imageURL || function (s, f, e) { return s + '/' + f + '/' + e; };

    function plantumlPlugin(md, options) {
        var opts = Object.assign({}, plantumlDefaults, options || {});

        var defaultFence = md.renderer.rules.fence ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.fence = function (tokens, idx, mdOptions, env, self) {
            var token = tokens[idx];
            var info = token.info ? token.info.trim().toLowerCase() : '';

            if (info !== 'plantuml' && info !== 'puml') {
                return defaultFence(tokens, idx, mdOptions, env, self);
            }

            var content = token.content;
            // Wrap in @startuml/@enduml if not already present.
            if (!/^@startuml/m.test(content)) {
                content = '@startuml\n' + content + '\n@enduml';
            }

            if (typeof plantumlEncoder === 'undefined' || !plantumlEncoder.encode) {
                return '<pre>' + md.utils.escapeHtml(token.content) + '</pre>\n';
            }

            var encoded = plantumlEncoder.encode(content);
            var url = plantumlImageURL(opts.server, opts.imageFormat, encoded);
            return '<p><img src="' + md.utils.escapeHtml(url) + '" alt="PlantUML diagram"></p>\n';
        };
    }

    window.plantumlPlugin = plantumlPlugin;
})();
