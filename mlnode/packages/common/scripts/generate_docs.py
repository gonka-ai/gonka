import argparse
import importlib
import json
import os
from pathlib import Path
from fastapi.openapi.utils import get_openapi


def import_app_from_string(app_path):
    """Import FastAPI app from a string like 'module_name:app_variable'."""
    if ":" not in app_path:
        raise ValueError("App path must be in format 'module:app_variable'")
    
    module_name, app_variable = app_path.split(":", 1)
    module = importlib.import_module(module_name)
    return getattr(module, app_variable)


def generate_openapi_json(app, output_dir="docs", title=None, version="1.0.0", description=None):
    print("Generating OpenAPI documentation...")
    
    docs_dir = Path(output_dir)
    docs_dir.mkdir(exist_ok=True)
    
    openapi_path = docs_dir / "openapi.json"

    if title is None:
        title = f"{app.__module__} API"
    
    if description is None:
        description = f"API documentation for {title}"
    
    openapi_schema = get_openapi(
        title=title,
        version=version,
        description=description,
        routes=app.routes,
    )
    
    with open(openapi_path, "w") as f:
        json.dump(openapi_schema, f, indent=2)
    
    print(f"OpenAPI documentation generated at {openapi_path}")


def main():
    parser = argparse.ArgumentParser(description="Generate OpenAPI/Swagger documentation from a FastAPI app")
    parser.add_argument("-m", "--module", required=True, help="Module path in format 'module:app', e.g., 'batch_receiver:app'")
    parser.add_argument("-o", "--output", default="docs", help="Output directory for OpenAPI JSON file")
    parser.add_argument("-t", "--title", help="API title (defaults to module name)")
    parser.add_argument("-v", "--version", default="1.0.0", help="API version")
    parser.add_argument("-d", "--description", help="API description")
    
    args = parser.parse_args()
    
    app = import_app_from_string(args.module)
    
    generate_openapi_json(
        app=app,
        output_dir=args.output,
        title=args.title,
        version=args.version,
        description=args.description
    )


if __name__ == "__main__":
    main()
