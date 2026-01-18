import { useState } from 'react'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Download, Loader2 } from 'lucide-react'

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
    <form onSubmit={handleSubmit} className="flex flex-col sm:flex-row gap-2">
      <div className="flex-1 min-w-0">
        <Input
          type="text"
          placeholder="Paste video URL (YouTube, Instagram, TikTok...)"
          value={url}
          onChange={(e) => {
            setUrl(e.target.value)
            if (error) setError('')
          }}
          disabled={isSubmitting}
          className={error ? 'border-destructive' : ''}
        />
        {error && (
          <p className="text-xs text-destructive mt-1">{error}</p>
        )}
      </div>
      <Button
        type="submit"
        disabled={isSubmitting}
        className="sm:w-auto w-full flex-shrink-0"
      >
        {isSubmitting ? (
          <>
            <Loader2 className="mr-2 h-4 w-4 animate-spin" />
            <span className="hidden sm:inline">Downloading...</span>
            <span className="sm:hidden">...</span>
          </>
        ) : (
          <>
            <Download className="mr-2 h-4 w-4" />
            Download
          </>
        )}
      </Button>
    </form>
  )
}
