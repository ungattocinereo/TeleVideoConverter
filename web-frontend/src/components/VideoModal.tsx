import {
  Dialog,
  DialogContent,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from '@/components/ui/dialog'
import { Button } from '@/components/ui/button'
import { Video } from '@/types'
import { formatSize, formatDate } from '@/lib/utils'
import { Download, Trash2, Film, ExternalLink } from 'lucide-react'

interface VideoModalProps {
  video: Video | null
  open: boolean
  onOpenChange: (open: boolean) => void
  onDownload: (video: Video) => void
  onDelete: (video: Video) => void
}

export function VideoModal({ video, open, onOpenChange, onDownload, onDelete }: VideoModalProps) {
  if (!video) return null

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-2xl">
        <DialogHeader>
          <DialogTitle>{video.title}</DialogTitle>
        </DialogHeader>

        <div className="space-y-4">
          {/* Thumbnail */}
          <div className="aspect-video bg-muted rounded-lg overflow-hidden">
            {video.thumbnail_url ? (
              <img
                src={video.thumbnail_url}
                alt={video.title}
                className="w-full h-full object-cover"
              />
            ) : (
              <div className="flex items-center justify-center h-full">
                <Film className="h-24 w-24 text-muted-foreground" />
              </div>
            )}
          </div>

          {/* Details */}
          <div className="grid grid-cols-2 gap-4 text-sm">
            <div>
              <div className="text-muted-foreground">Original URL</div>
              <a
                href={video.original_url}
                target="_blank"
                rel="noopener noreferrer"
                className="flex items-center gap-1 text-primary hover:underline"
              >
                Open Link <ExternalLink className="h-3 w-3" />
              </a>
            </div>

            <div>
              <div className="text-muted-foreground">Downloaded</div>
              <div>{formatDate(video.download_timestamp)}</div>
            </div>

            <div>
              <div className="text-muted-foreground">Deletes</div>
              <div>{formatDate(video.delete_timestamp)}</div>
            </div>

            <div>
              <div className="text-muted-foreground">Time Remaining</div>
              <div>{video.time_remaining}</div>
            </div>

            <div>
              <div className="text-muted-foreground">File Size</div>
              <div>{formatSize(video.file_size)}</div>
            </div>

            <div>
              <div className="text-muted-foreground">Format</div>
              <div>{video.format} ({video.codec || 'N/A'})</div>
            </div>

            <div>
              <div className="text-muted-foreground">Quality</div>
              <div>{video.downloaded_quality}</div>
            </div>

            {video.original_quality && (
              <div>
                <div className="text-muted-foreground">Original Quality</div>
                <div>{video.original_quality}</div>
              </div>
            )}

            {video.source_platform && (
              <div>
                <div className="text-muted-foreground">Source</div>
                <div>{video.source_platform}</div>
              </div>
            )}
          </div>
        </div>

        <DialogFooter>
          <Button variant="outline" onClick={() => onOpenChange(false)}>
            Close
          </Button>
          <Button variant="destructive" onClick={() => {
            onDelete(video)
            onOpenChange(false)
          }}>
            <Trash2 className="h-4 w-4 mr-2" />
            Delete
          </Button>
          <Button onClick={() => {
            onDownload(video)
          }}>
            <Download className="h-4 w-4 mr-2" />
            Download
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
