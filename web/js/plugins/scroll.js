// scroll.js -- Scroll synchronization for vim-markdown-preview.
// Maps editor cursor positions to rendered DOM elements via
// data-source-line attributes and scrolls the preview window.
// Three sync modes: middle (center), top (cursor at top), relative (proportional).
(function () {
    'use strict';

    // Find all elements with data-source-line, sorted by line number.
    function getSourceLineElements() {
        var elements = document.querySelectorAll('[data-source-line]');
        var result = [];
        elements.forEach(function (el) {
            var line = parseInt(el.getAttribute('data-source-line'), 10);
            if (!isNaN(line)) {
                result.push({ line: line, element: el });
            }
        });
        result.sort(function (a, b) { return a.line - b.line; });
        return result;
    }

    // Find the element at or just before the given source line.
    function findClosestElement(lineElements, targetLine) {
        var best = null;
        var bestNext = null;
        for (var i = 0; i < lineElements.length; i++) {
            if (lineElements[i].line <= targetLine) {
                best = lineElements[i];
                bestNext = (i + 1 < lineElements.length) ? lineElements[i + 1] : null;
            } else {
                if (!bestNext) bestNext = lineElements[i];
                break;
            }
        }
        return { current: best, next: bestNext };
    }

    // Scroll to the position corresponding to the editor cursor.
    // cursor: getpos(".") array [bufnum, lnum, col, off]
    // winline: cursor screen line within window
    // winheight: total window height
    // totalLines: total document lines
    // scrollType: 'middle', 'top', or 'relative'
    function scrollToLine(cursor, winline, winheight, totalLines, scrollType) {
        if (!cursor || !Array.isArray(cursor) || cursor.length < 2) return;

        var lineElements = getSourceLineElements();
        if (lineElements.length === 0) return;

        // Cursor line is 1-indexed in Vim; data-source-line is 0-indexed.
        var cursorLine = cursor[1] - 1;
        if (cursorLine < 0) cursorLine = 0;

        var match = findClosestElement(lineElements, cursorLine);
        if (!match.current) return;

        var targetTop = match.current.element.offsetTop;

        // Interpolate between current and next element for smoother scrolling.
        if (match.next && match.next.line > match.current.line) {
            var fraction = (cursorLine - match.current.line) /
                (match.next.line - match.current.line);
            var nextTop = match.next.element.offsetTop;
            targetTop = targetTop + (nextTop - targetTop) * fraction;
        }

        var viewportHeight = window.innerHeight;
        var scrollTarget;

        switch (scrollType) {
            case 'top':
                scrollTarget = targetTop;
                break;
            case 'relative':
                // Position the target proportionally: if cursor is at winline
                // out of winheight, place the element at the same relative
                // position in the viewport. Use (winheight - 1) as the
                // denominator so that the last visible line (winline == winheight)
                // maps to ratio 1.0 rather than (N-1)/N.
                if (winheight > 1 && winline > 0) {
                    var ratio = (winline - 1) / (winheight - 1);
                    scrollTarget = targetTop - (viewportHeight * ratio);
                } else {
                    scrollTarget = targetTop - viewportHeight / 2;
                }
                break;
            case 'middle':
            default:
                scrollTarget = targetTop - viewportHeight / 2;
                break;
        }

        // Clamp to valid scroll range.
        var maxScroll = document.documentElement.scrollHeight - viewportHeight;
        if (scrollTarget < 0) scrollTarget = 0;
        if (scrollTarget > maxScroll) scrollTarget = maxScroll;

        window.scrollTo({ top: scrollTarget, behavior: 'smooth' });
    }

    window.scrollToLine = scrollToLine;
})();
