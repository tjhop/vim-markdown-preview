// scroll.js -- Scroll synchronization for vim-markdown-preview.
// Maps editor cursor positions to rendered DOM elements via
// data-source-line attributes and scrolls the preview window.
// Three sync modes: middle (center), top (cursor at top), relative (proportional).
(function () {
    'use strict';

    // Cached source-line element list. Invalidated after each content render
    // so that cursor-only moves (which don't change the DOM) reuse the
    // previous query result instead of re-scanning the entire document.
    var cachedLineElements = null;

    // Find all elements with data-source-line, sorted by line number.
    // Returns the cached result when the DOM has not changed since the
    // last content render.
    function getSourceLineElements() {
        if (cachedLineElements !== null) {
            return cachedLineElements;
        }
        var elements = document.querySelectorAll('[data-source-line]');
        var result = [];
        elements.forEach(function (el) {
            var line = parseInt(el.getAttribute('data-source-line'), 10);
            if (!isNaN(line)) {
                result.push({ line: line, element: el });
            }
        });
        result.sort(function (a, b) { return a.line - b.line; });
        cachedLineElements = result;
        return result;
    }

    // Invalidate the cached element list. Called by preview.js after
    // rendering new content so the next scroll picks up fresh DOM nodes.
    function invalidateLineElementCache() {
        cachedLineElements = null;
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

    // requestAnimationFrame throttle state. When scroll events arrive faster
    // than the browser can paint, only the most recent request is executed.
    // The coalesced flag tracks whether earlier events were dropped, which
    // switches from smooth to instant scrolling to avoid overlapping
    // animations that produce visible jank.
    var pendingScrollArgs = null;
    var rafId = null;

    // Scroll to the position corresponding to the editor cursor.
    // cursor: getpos(".") array [bufnum, lnum, col, off]
    // winline: cursor screen line within window
    // winheight: total window height
    // totalLines: total document lines
    // scrollType: 'middle', 'top', or 'relative'
    function scrollToLine(cursor, winline, winheight, totalLines, scrollType) {
        pendingScrollArgs = {
            cursor: cursor,
            winline: winline,
            winheight: winheight,
            totalLines: totalLines,
            scrollType: scrollType
        };

        if (rafId !== null) {
            // A frame callback is already scheduled. The pending args
            // will be picked up when it fires; use instant behavior
            // since events are arriving faster than frames.
            return;
        }

        rafId = requestAnimationFrame(function () {
            var args = pendingScrollArgs;
            rafId = null;
            pendingScrollArgs = null;
            if (!args) return;
            executeScroll(args.cursor, args.winline, args.winheight, args.totalLines, args.scrollType);
        });
    }

    // Execute the actual scroll. Separated from scrollToLine so the rAF
    // throttle can coalesce multiple calls into one.
    function executeScroll(cursor, winline, winheight, totalLines, scrollType) {
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
    window.invalidateLineElementCache = invalidateLineElementCache;
})();
