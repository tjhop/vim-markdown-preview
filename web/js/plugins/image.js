// image.js -- Local image path rewriting plugin for markdown-it.
// Detects image src attributes that are local file paths (not http/https/data)
// and rewrites them to the server's /_local_image/ proxy endpoint.
(function () {
    'use strict';

    function imagePlugin(md) {
        var defaultRender = md.renderer.rules.image ||
            function (tokens, idx, options, env, self) {
                return self.renderToken(tokens, idx, options);
            };

        md.renderer.rules.image = function (tokens, idx, options, env, self) {
            var token = tokens[idx];
            var srcIndex = token.attrIndex('src');

            if (srcIndex >= 0) {
                var src = token.attrs[srcIndex][1];
                // Only rewrite paths that are not absolute URLs, protocol-relative
                // URLs (//example.com/...), data URIs, or absolute filesystem
                // paths (starting with /). Absolute paths cannot be served
                // through the local image proxy and would always be rejected.
                if (src && !/^(https?:\/\/|\/\/|\/|data:)/i.test(src)) {
                    // Normalize backslash separators (Windows-style paths) to
                    // forward slashes before encoding. Encode each path segment
                    // individually to preserve directory separators while encoding
                    // special characters (#, ?, %, spaces) that would otherwise
                    // produce malformed URLs.
                    var encoded = src.replace(/\\/g, '/').split('/').map(encodeURIComponent).join('/');
                    token.attrs[srcIndex][1] = '/_local_image/' + encoded;
                }
            }

            return defaultRender(tokens, idx, options, env, self);
        };
    }

    window.imagePlugin = imagePlugin;
})();
