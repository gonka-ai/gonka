from __future__ import annotations

from io import BytesIO
from pathlib import Path

from datasets import DatasetDict, load_dataset
from PIL import Image as PILImage


def _ensure_pil_image(image):
    if isinstance(image, dict) and image.get("bytes"):
        return PILImage.open(BytesIO(image["bytes"]))
    return image


def _extension_for_image(img: PILImage.Image) -> str:
    fmt = getattr(img, "format", None)
    if fmt:
        return "jpg" if fmt.upper() == "JPEG" else fmt.lower()
    return "jpg"


def _save_image(img: PILImage.Image, path: Path, jpeg_quality: int = 95) -> None:
    ext = path.suffix.lower().lstrip(".")
    kwargs: dict = {}
    if ext in ("jpg", "jpeg"):
        kwargs["quality"] = jpeg_quality
        if img.mode in ("RGBA", "P"):
            img = img.convert("RGB")
    img.save(path, **kwargs)


def export_parquet_images_to_files(
    data,
    out_dir: str = "flickr8k_images",
    *,
    jpeg_quality: int = 95,
) -> None:
    """Сохраняет раскодированные картинки из Dataset/DatasetDict в файлы."""
    root = Path(out_dir)
    root.mkdir(parents=True, exist_ok=True)

    if isinstance(data, DatasetDict):
        splits = list(data.items())
    else:
        splits = [("train", data)]

    for split_name, ds in splits:
        split_dir = root / split_name
        split_dir.mkdir(parents=True, exist_ok=True)
        for i, row in enumerate(ds):
            img = _ensure_pil_image(row["image"])
            ext = _extension_for_image(img)
            out_path = split_dir / f"{i:05d}.{ext}"
            _save_image(img, out_path, jpeg_quality=jpeg_quality)


ds = load_dataset("jxie/flickr8k")
ds.save_to_disk("flickr8k")
export_parquet_images_to_files(ds, "flickr8k_images")
