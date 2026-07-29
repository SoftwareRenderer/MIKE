# CSS Extensions

CSS extensions allow users to customize the visual appearance of the [MIKE](https://getmike.dev) UI by providing custom stylesheets.

## Overview

CSS extensions consist of a CSS file and a manifest describing the extension. The Go server serves these files as static assets, and the frontend loads the specified CSS file.

## Structure

A CSS extension is organized as a directory within `extensions/css/`:

```text
extensions/css/<name>/
├── manifest.json
└── styles.css
```

### manifest.json

The manifest provides metadata and tells the system where the CSS file is located.

```json
{
    "display_name": "My Theme",
    "description": "A custom color scheme",
    "author": "Author Name",
    "version": "1.0.0",
    "capabilities": {
        "css": {
            "css_url": "/extensions/css/<name>/styles.css"
        }
    }
}
```

### styles.css

This file contains the standard CSS rules to override the default styles of the application.
