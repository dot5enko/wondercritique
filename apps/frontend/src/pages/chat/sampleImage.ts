/**
 * Prepares a picked style-guide sample for upload. Tablet / phone pickers hand
 * over camera photos and full-resolution screenshots (often several MB, HEIC
 * on iPad, sometimes with an empty `type`), and the upload goes through proxies
 * capped at ~10 MB per request. So anything that isn't already a small
 * PNG/JPEG/WebP/GIF is decoded and re-encoded as a JPEG with its long edge
 * ≤ MAX_EDGE — plenty for the vision model, which downscales anyway.
 */

/** Long edge of a re-encoded sample, px. */
const MAX_EDGE = 2048
/** Files of a sent-as-is type up to this size are kept untouched. */
const KEEP_BYTES = 1.5 * 1024 * 1024
const SENDABLE = new Set(['image/png', 'image/jpeg', 'image/webp', 'image/gif'])
const IMAGE_EXT = /\.(png|jpe?g|webp|gif|heic|heif|avif|bmp|tiff?)$/i

/** True for anything that looks like an image (mobile pickers may leave `type` empty). */
export function isImageFile(f: File): boolean {
  return f.type.startsWith('image/') || (f.type === '' && IMAGE_EXT.test(f.name))
}

interface Decoded {
  source: CanvasImageSource
  width: number
  height: number
  close: () => void
}

async function decode(file: File): Promise<Decoded> {
  if (typeof createImageBitmap === 'function') {
    try {
      const bmp = await createImageBitmap(file)
      return { source: bmp, width: bmp.width, height: bmp.height, close: () => bmp.close() }
    } catch {
      // fall through: older Safari can't make bitmaps from some blobs
    }
  }
  const url = URL.createObjectURL(file)
  try {
    const img = new Image()
    img.src = url
    await img.decode()
    return { source: img, width: img.naturalWidth, height: img.naturalHeight, close: () => {} }
  } finally {
    URL.revokeObjectURL(url)
  }
}

/**
 * Returns the file to upload: the original when it's a small sendable image
 * (GIFs are always kept — re-encoding would lose the format), otherwise a
 * downscaled JPEG. Throws when the browser can't decode the image.
 */
export async function prepareSampleImage(file: File): Promise<File> {
  if (SENDABLE.has(file.type) && (file.size <= KEEP_BYTES || file.type === 'image/gif')) return file
  const img = await decode(file)
  try {
    const scale = Math.min(1, MAX_EDGE / Math.max(img.width, img.height))
    const w = Math.max(1, Math.round(img.width * scale))
    const h = Math.max(1, Math.round(img.height * scale))
    const canvas = document.createElement('canvas')
    canvas.width = w
    canvas.height = h
    const ctx = canvas.getContext('2d')
    if (!ctx) throw new Error('canvas unavailable')
    ctx.fillStyle = '#fff' // JPEG has no alpha
    ctx.fillRect(0, 0, w, h)
    ctx.drawImage(img.source, 0, 0, w, h)
    const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/jpeg', 0.9))
    if (!blob) throw new Error('encode failed')
    const base = file.name.replace(/\.[^.]*$/, '') || 'image'
    return new File([blob], `${base}.jpg`, { type: 'image/jpeg' })
  } finally {
    img.close()
  }
}
