from fastapi.openapi.utils import get_openapi
import json
import os
from pathlib import Path

from pow.app import app


def generate_openapi_json():
    """Generate OpenAPI documentation JSON file."""
    print("Generating OpenAPI documentation...")
    
    # Create docs directory if it doesn't exist
    docs_dir = Path("docs")
    docs_dir.mkdir(exist_ok=True)
    
    openapi_path = docs_dir / "openapi.json"
    
    # Generate OpenAPI schema
    openapi_schema = get_openapi(
        title="Proof of Work API",
        version="1.0.0",
        description="API for Proof of Work operations",
        routes=app.routes,
    )
    
    # Write to file
    with open(openapi_path, "w") as f:
        json.dump(openapi_schema, f, indent=2)
    
    print(f"OpenAPI documentation generated at {openapi_path}")


if __name__ == "__main__":
    generate_openapi_json()
