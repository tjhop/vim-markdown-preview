// linenumbers.js -- Source line number injection plugin for markdown-it.
// Adds data-source-line attributes to block-level tokens so that scroll
// sync can map cursor positions to rendered DOM elements.
(function () {
    'use strict';

    var tokenTypes = [
        'paragraph_open',
        'heading_open',
        'list_item_open',
        'table_open',
        'blockquote_open',
        'hr',
        'code_block',
        'fence',
        'html_block'
    ];

    function linenumbersPlugin(md) {
        tokenTypes.forEach(function (type) {
            var defaultRender = md.renderer.rules[type];

            md.renderer.rules[type] = function (tokens, idx, options, env, self) {
                var token = tokens[idx];
                if (token.map && token.map.length > 0) {
                    token.attrJoin('class', 'source-line');
                    token.attrSet('data-source-line', String(token.map[0]));
                }
                if (defaultRender) {
                    return defaultRender(tokens, idx, options, env, self);
                }
                return self.renderToken(tokens, idx, options);
            };
        });
    }

    window.linenumbersPlugin = linenumbersPlugin;
})();
