// Copyright The Myriarch Authors
// SPDX-License-Identifier: Apache-2.0

// Override of the Docsy v0.18.0 script assets/js/offline-search.js (Apache-2.0, derived work; see website/overrides.txt).
// Change against the upstream file: the search index is fetched, and the lunr index built, on the first use of a search box
// instead of on every page load. The build of the index is one long task (about 0.5 s on a phone) and the only one of the
// landing page, so it cost Total Blocking Time and a part of the performance score on pages where most visitors never search.
// The changed parts are marked "Changed from upstream"; the search itself (rendering, popover, submit handling, maximum number
// of results) is the upstream code.
// Re-check this file against the upstream one whenever Docsy is updated (website/scripts/check-overrides.py).

// Adapted from code by Matt Walters https://www.mattwalters.net/posts/2018-03-28-hugo-and-lunr/

(function () {
  'use strict';

  function ready(fn) {
    if (document.readyState !== 'loading') {
      fn();
    } else {
      document.addEventListener('DOMContentLoaded', fn);
    }
  }

  ready(function () {
    const searchInputs = Array.from(
      document.querySelectorAll('.td-search input'),
    );
    if (searchInputs.length === 0) {
      return;
    }

    //
    // Register handlers
    //

    searchInputs.forEach((input) => {
      input.addEventListener('change', () => {
        // Changed from upstream: the index may not be there yet when the first
        // query comes in. A query that is typed or submitted while it loads is
        // rendered as soon as the index is ready (see loadIndex).
        if (idx === null && input.value !== '') {
          pendingInputs.add(input);
          loadIndex();
        }

        render(input);

        // Hide keyboard on mobile browser
        input.blur();
      });

      // Prevent reloading page by enter key on sidebar search.
      // stopPropagation preserves the jQuery handler's `return false`
      // semantics: site-level delegated submit listeners never saw this
      // event before the conversion.
      const form = input.closest('form');
      if (form) {
        form.addEventListener('submit', (e) => {
          e.preventDefault();
          e.stopPropagation();
        });
      }
    });

    //
    // Lunr
    //

    let idx = null; // Lunr index
    const resultDetails = new Map(); // Will hold the data for the search results (titles and summaries)

    // Changed from upstream: the index is requested and built on the first use
    // of a search box, not when the page has loaded. All search boxes share one
    // promise, so the index is fetched and built once, however often they are
    // used. A failed load is not retried, as before (one attempt per page).
    let indexPromise = null;
    const pendingInputs = new Set(); // Search boxes with a query that waits for the index

    const loadIndex = () => {
      if (indexPromise !== null) {
        return indexPromise;
      }

      // Request the JSON data file that is created by Hugo's build process
      indexPromise = fetch(searchInputs[0].dataset.offlineSearchIndexJsonSrc)
        .then((response) => {
          if (!response.ok) {
            throw new Error(`HTTP status ${response.status}`);
          }
          return response.json();
        })
        .then((data) => {
          idx = lunr(function () {
            this.ref('ref');

            // If you added more searchable fields to the search index, list them here.
            // Here you can specify searchable fields to the search index - e.g. individual toxonomies for you project
            // With "boost" you can add weighting for specific (default weighting without boost: 1)
            this.field('title', { boost: 5 });
            this.field('categories', { boost: 3 });
            this.field('tags', { boost: 3 });
            // this.field('projects', { boost: 3 }); // example for an individual toxonomy called projects
            this.field('description', { boost: 2 });
            this.field('body');

            data.forEach((doc) => {
              this.add(doc);

              resultDetails.set(doc.ref, {
                title: doc.title,
                excerpt: doc.excerpt,
              });
            });
          });
        })
        .catch((error) => {
          console.error('Could not load the search index:', error);
        });

      // Changed from upstream: upstream dispatched a change event on every
      // search box here, which also blurred the box. A box that is in use
      // while the index arrives must keep its focus, so only the queries that
      // were submitted in the meantime are rendered, and without a blur (the
      // box was blurred when the query was submitted). This is a separate step
      // so that an error of render is not reported as a failed index load.
      indexPromise.then(() => {
        pendingInputs.forEach((input) => render(input));
        pendingInputs.clear();
      });

      return indexPromise;
    };

    // Changed from upstream: the first use of any search box starts the load:
    // focus (keyboard, tap or script), pointerdown (mouse, pen or touch, which
    // comes before the focus) or input (typing, pasting, autofill). Whichever
    // comes first removes all of these listeners.
    const firstUseEvents = ['focus', 'pointerdown', 'input'];
    const onFirstUse = () => {
      searchInputs.forEach((input) => {
        firstUseEvents.forEach((type) => {
          input.removeEventListener(type, onFirstUse);
        });
      });
      loadIndex();
    };
    searchInputs.forEach((input) => {
      firstUseEvents.forEach((type) => {
        input.addEventListener(type, onFirstUse);
      });
    });

    const render = (targetInput) => {
      //
      // Dispose existing popover
      //

      {
        const popover = bootstrap.Popover.getInstance(targetInput);
        if (popover !== null) {
          popover.dispose();
        }
      }

      //
      // Search
      //

      if (idx === null) {
        return;
      }

      const searchQuery = targetInput.value;
      if (searchQuery === '') {
        return;
      }

      // An absent max-results attribute (a customized search-input
      // partial) means unlimited, as the jQuery-era data read had it;
      // NaN would silently slice to zero results.
      const maxResults = parseInt(
        targetInput.dataset.offlineSearchMaxResults,
        10,
      );

      const results = idx
        .query((q) => {
          const tokens = lunr.tokenizer(searchQuery.toLowerCase());
          tokens.forEach((token) => {
            const queryString = token.toString();
            q.term(queryString, {
              boost: 100,
            });
            q.term(queryString, {
              wildcard:
                lunr.Query.wildcard.LEADING | lunr.Query.wildcard.TRAILING,
              boost: 10,
            });
            q.term(queryString, {
              editDistance: 2,
            });
          });
        })
        .slice(0, Number.isNaN(maxResults) ? undefined : maxResults);

      //
      // Make result html
      //
      // Result values (query, titles, excerpts) are user- or
      // index-controlled: only ever assign them via textContent.
      //

      const html = document.createElement('div');

      const header = document.createElement('div');
      Object.assign(header.style, {
        display: 'flex',
        justifyContent: 'space-between',
        marginBottom: '1em',
      });

      const headerTitle = document.createElement('span');
      headerTitle.textContent = 'Search results';
      headerTitle.style.fontWeight = 'bold';

      const closeButton = document.createElement('span');
      closeButton.classList.add('td-offline-search-results__close-button');

      header.append(headerTitle, closeButton);
      html.append(header);

      const searchResultBody = document.createElement('div');
      Object.assign(searchResultBody.style, {
        maxHeight: `calc(100vh - ${
          targetInput.getBoundingClientRect().top + 180
        }px)`,
        overflowY: 'auto',
      });
      html.append(searchResultBody);

      if (results.length === 0) {
        const noResults = document.createElement('p');
        noResults.textContent = `No results found for query "${searchQuery}"`;
        searchResultBody.append(noResults);
      } else {
        results.forEach((r) => {
          const doc = resultDetails.get(r.ref);
          // First input's base-href, like the jQuery collection read.
          const href =
            searchInputs[0].dataset.offlineSearchBaseHref +
            r.ref.replace(/^\//, '');

          const entry = document.createElement('div');
          entry.classList.add('mt-4');

          const entryRef = document.createElement('small');
          entryRef.classList.add('d-block', 'text-body-secondary');
          entryRef.textContent = r.ref;

          const entryLink = document.createElement('a');
          entryLink.classList.add('d-block');
          entryLink.style.fontSize = '1.2rem';
          entryLink.href = href;
          entryLink.textContent = doc.title;

          const entryExcerpt = document.createElement('p');
          entryExcerpt.textContent = doc.excerpt;

          entry.append(entryRef, entryLink, entryExcerpt);
          searchResultBody.append(entry);
        });
      }

      targetInput.addEventListener(
        'shown.bs.popover',
        () => {
          document
            .querySelectorAll('.td-offline-search-results__close-button')
            .forEach((button) => {
              button.addEventListener('click', () => {
                targetInput.value = '';
                targetInput.dispatchEvent(
                  new Event('change', { bubbles: true }),
                );
              });
            });
        },
        { once: true },
      );

      const popover = new bootstrap.Popover(targetInput, {
        content: html,
        html: true,
        customClass: 'td-offline-search-results',
        placement: 'bottom',
      });
      popover.show();
    };
  });
})();
