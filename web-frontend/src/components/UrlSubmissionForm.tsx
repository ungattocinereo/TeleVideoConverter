import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Link, Download, Loader2 } from 'lucide-react'

interface UrlSubmissionFormProps {
  onSubmit: (url: string, quality: string) => Promise<void>
}

export function UrlSubmissionForm({ onSubmit }: UrlSubmissionFormProps) {
  const [url, setUrl] = useState('')
  const [isSubmitting, setIsSubmitting] = useState(false)
  const [error, setError] = useState('')

  const handleSubmit = async (e: React.FormEvent) => {
    e.preventDefault()

    if (!url.trim()) {
      setError('Please enter a video URL')
      return
    }

    if (!url.startsWith('http://') && !url.startsWith('https://')) {
      setError('Please enter a valid URL starting with http:// or https://')
      return
    }

    setError('')
    setIsSubmitting(true)

    try {
      // Always use 'best' quality
      await onSubmit(url, 'best')
      setUrl('') // Clear the input on success
    } catch (err) {
      setError(err instanceof Error ? err.message : 'Failed to submit URL')
    } finally {
      setIsSubmitting(false)
    }
  }

  return (
    <div className="bg-card border rounded-lg p-6 mb-6">
      <div className="flex items-center gap-2 mb-4">
        <Link className="h-5 w-5 text-primary" />
        <h2 className="text-xl font-semibold">Download Video</h2>
      </div>

      <form onSubmit={handleSubmit} className="space-y-4">
        <div>
          <Input
            type="text"
            placeholder="Enter video URL (YouTube, Instagram, Vimeo, etc.)"
            value={url}
            onChange={(e) => setUrl(e.target.value)}
            disabled={isSubmitting}
            className="w-full"
          />
          {error && (
            <p className="text-sm text-destructive mt-2">{error}</p>
          )}
          <p className="text-xs text-muted-foreground mt-2">
            Video will be downloaded in the best available quality
          </p>
        </div>

        <Button
          type="submit"
          className="w-full"
          disabled={isSubmitting}
        >
          {isSubmitting ? (
            <>
              <Loader2 className="mr-2 h-4 w-4 animate-spin" />
              Submitting...
            </>
          ) : (
            <>
              <Download className="mr-2 h-4 w-4" />
              Download Video
            </>
          )}
        </Button>
      </form>
    </div>
  )
}
