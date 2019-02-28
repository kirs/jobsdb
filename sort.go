package main

func FrontBackSplit(source *jobListNode) (*jobListNode, *jobListNode) {
	var fast *jobListNode
	var slow *jobListNode

	slow = source
	fast = source.Next

	for fast != nil {
		fast = fast.Next
		if fast != nil {
			slow = slow.Next
			fast = fast.Next
		}
	}

	/* 'slow' is before the midpoint in the list, so split it in two
	at that point. */
	frontRef := source
	backRef := slow.Next
	slow.Next = nil
	return frontRef, backRef
}

func SortedMerge(a *jobListNode, b *jobListNode) *jobListNode {
	/* See https://www.geeksforgeeks.org/?p=3622 for details of this
	function */
	//struct Node* SortedMerge(struct Node* a, struct Node* b)
	//{
	var result *jobListNode

	/* Base cases */
	if a == nil {
		return (b)
	} else if b == nil {
		return (a)
	}

	/* Pick either a or b, and recur */
	if a.Job.job.Priority <= b.Job.job.Priority {
		result = a
		result.Next = SortedMerge(a.Next, b)
	} else {
		result = b
		result.Next = SortedMerge(a, b.Next)
	}
	return result
}

// MergeSort(headRef)
// 1) If head is NULL or there is only one element in the Linked List
//     then return.
// 2) Else divide the linked list into two halves.
//       FrontBackSplit(head, &a, &b); /* a and b are two halves */
// 3) Sort the two halves a and b.
//       MergeSort(a);
//       MergeSort(b);
// 4) Merge the sorted a and b (using SortedMerge() discussed here)
//    and update the head pointer using headRef.
//      *headRef = SortedMerge(a, b);

func MergeSort(head *jobListNode) *jobListNode {
	if head == nil || head.Next == nil {
		return head
	}
	a, b := FrontBackSplit(head)
	a = MergeSort(a)
	b = MergeSort(b)
	return SortedMerge(a, b)
}
