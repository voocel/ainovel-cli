# Mẫu quy hoạch đề cương

Tác dụng của mẫu này không phải là ép tất cả tác phẩm vào một độ dài cố định, mà là giúp đánh giá cấp độ tác phẩm trước, sau đó mới lựa chọn độ chi tiết của đề cương.

## Bước thứ nhất: Đánh giá cấp độ độ dài tác phẩm

### Truyện ngắn / Câu chuyện đơn quyển

- Áp dụng: Xung đột đơn, mục tiêu đơn, ít nhân vật, kết cục tập trung
- Độ dài tham khảo: 8-25 chương
- Định dạng khuyên dùng: `outline` phẳng

### Truyện vừa / Câu chuyện nhiều giai đoạn

- Áp dụng: Có thăng cấp giai đoạn, vài tuyến phụ, mối quan hệ nhân vật sẽ thay đổi
- Độ dài tham khảo: 25-60 chương
- Định dạng khuyên dùng: `outline` phẳng hoặc phân tầng nhẹ

### Truyện dài kỳ / Câu chuyện kiểu mạng internet

- Áp dụng: Đề tài tự nhiên có không gian thăng cấp liên tục, căng thẳng quan hệ lâu dài, nhiều mục tiêu giai đoạn, thế giới có thể mở rộng, bí ẩn lâu dài hoặc tuyến trưởng thành lâu dài
- Độ dài tham khảo: 80-200+ chương
- Định dạng khuyên dùng: Phân tầng `layered_outline`

## Bước thứ hai: Phán đoán có bắt buộc phải sử dụng đề cương phân tầng không

Chỉ cần thỏa mãn bất kỳ 2 điều kiện nào dưới đây thì ưu tiên sử dụng `layered_outline`:

- Thế giới quan cần mở ra từng bước, không phải nói hết một lần
- Sự trưởng thành của nhân vật chính không phải là một lần nhảy vọt, mà là thăng cấp nhiều giai đoạn
- Mối quan hệ nhân vật sẽ liên tục thay đổi trong nhiều giai đoạn
- Giai đoạn giữa và giai đoạn cuối tồn tại các mâu thuẫn chính thuộc loại hình khác nhau
- Cần chuyển đổi bản đồ/thế lực/thân phận/mục tiêu nhiều lần
- Đề tài rõ ràng giống như tiểu thuyết thương mại dài kỳ hơn là câu chuyện đơn quyển

## Bước thứ ba: Khi viết truyện dài đừng trực tiếp làm "sổ tay ghi chép chương toàn thư"

Thứ tự quy hoạch khuyên dùng cho truyện dài là:

1. Điểm bán hàng và khác biệt hóa của tác phẩm
2. Động cơ câu chuyện dài hạn
3. Chủ đề và nâng cấp cấp quyển
4. Mục tiêu cấp phân đoạn (arc) và bước ngoặt giai đoạn
5. Sự kiện và móc treo cấp chương

Cách làm sai:

- Viết 20 chương tóm tắt trước, sau đó cưỡng ép kéo dài ra
- Mỗi quyển đều lặp lại "gặp địch - mạnh lên - đổi bản đồ"
- Chỉ có nâng cấp tuyến chính, không có nâng cấp mối quan hệ
- Giai đoạn đầu tiêu tốn hết mọi bí mật lớn, giai đoạn giữa và cuối chỉ có thể lặp lại lối mòn

## Mẫu đề cương phẳng (Truyện ngắn/vừa)

```json
[
  {
    "chapter": 1,
    "title": "Tiêu đề chương",
    "core_event": "Sự kiện cốt lõi của chương",
    "hook": "Móc treo cuối chương",
    "scenes": ["Cảnh 1", "Cảnh 2", "Cảnh 3"]
  }
]
```

## Mẫu đề cương phân tầng (Truyện dài - Cuộn tròn hai lớp quyển và phân đoạn)

Quy hoạch ban đầu áp dụng cuộn tròn hai lớp: 2 quyển đầu có khung phân đoạn, các quyển còn lại là quyển khung xương; phân đoạn thứ nhất có chương chi tiết.

```json
[
  {
    "index": 1,
    "title": "Tiêu đề quyển một",
    "theme": "Mâu thuẫn/chủ đề cốt lõi mới tăng thêm của quyển này",
    "arcs": [
      {
        "index": 1,
        "title": "Phân đoạn thứ nhất (Đã triển khai)",
        "goal": "Mục tiêu cục bộ, lực cản và bước ngoặt",
        "chapters": [
          {"chapter": 1, "title": "Tiêu đề chương", "core_event": "Sự kiện cốt lõi", "hook": "Móc treo cuối chương", "scenes": ["Cảnh 1", "Cảnh 2"]}
        ]
      },
      {
        "index": 2,
        "title": "Phân đoạn thứ hai (Phân đoạn khung xương)",
        "goal": "Khái quát mục tiêu của phân đoạn này",
        "estimated_chapters": 12,
        "chapters": []
      }
    ]
  },
  {
    "index": 2,
    "title": "Tiêu đề quyển hai",
    "theme": "Chủ đề quyển hai",
    "arcs": [
      {"index": 1, "title": "Tiêu đề phân đoạn", "goal": "Mục tiêu phân đoạn", "estimated_chapters": 15, "chapters": []},
      {"index": 2, "title": "Tiêu đề phân đoạn", "goal": "Mục tiêu phân đoạn", "estimated_chapters": 10, "chapters": []}
    ]
  },
  {
    "index": 3,
    "title": "Tiêu đề quyển ba (Quyển khung xương)",
    "theme": "Hướng chủ đề quyển ba",
    "estimated_chapters": 60,
    "arcs": []
  }
]
```

- Triển khai cấp phân đoạn (arc): Khi việc viết tiến tới phân đoạn khung xương, Architect triển khai các chương chi tiết của phân đoạn đó
- Triển khai cấp quyển: Khi việc viết tiến tới quyển khung xương, Architect triển khai cấu trúc phân đoạn của quyển đó + các chương của phân đoạn đầu tiên

## Danh sách kiểm tra cấp quyển truyện dài

Mỗi quyển đều phải trả lời:

- Quyển này bổ sung thêm thông tin thế giới nào mới?
- Quyển này nâng cấp mâu thuẫn cốt lõi nào mới?
- Quyển này giúp nhân vật chính đạt được gì, và mất đi gì?
- Quyển này thay đổi mối quan hệ nhân vật chính như thế nào?
- Sau khi quyển này kết thúc, tại sao câu chuyện bắt buộc phải tiến vào quyển tiếp theo?

## Danh sách kiểm tra cấp phân đoạn truyện dài

Mỗi phân đoạn đều phải trả lời:

- Mục tiêu rõ ràng của phân đoạn này là gì?
- Lực cản đến từ ai, quy tắc nào, cái giá nào?
- Điểm bước ngoặt là gì?
- Sau khi phân đoạn này kết thúc, những trạng thái nào đã xảy ra thay đổi không thể đảo ngược?

## Danh sách kiểm tra cấp chương

- Mỗi chương bắt buộc phải phục vụ cho mục tiêu của phân đoạn chứa nó
- Mỗi chương bắt buộc phải chứa một sự thúc đẩy sự kiện không thể xóa bỏ
- Móc treo phải đa dạng hóa, đừng chỉ dựa vào một mô thức "phát hiện bí mật"
- Các chương giai đoạn đầu không thể chỉ đơn thuần "giới thiệu thế giới", bắt buộc phải thúc đẩy đồng bộ nhân vật và xung đột